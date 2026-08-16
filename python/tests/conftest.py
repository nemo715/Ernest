"""Shared fixtures: an in-process mock ernest backend.

Replicates the wire contract of ``internal/server/server.go`` exactly as
``web/scripts/mock-server.mjs`` does — same routes, same JSON keys, same
event ordering:

- ``GET /healthz``, ``GET /api/agents``
- ``POST /api/chat``  → SSE: run.start → deltas → message.complete →
  tool.call → approval.requested → run.complete(awaiting_approval)
- ``POST /api/approve`` → SSE: approval.resolved → tool.result →
  run.complete(completed, with usage)
- sessions list/get/delete

Error-path hooks (driven by the chat request body) for typed-exception
tests:

- ``input == "boom"``          → ``run.error`` kind ``agent_error``
- ``input == "tool-boom"``     → ``run.error`` kind ``tool_error``
- ``input == "provider-boom"`` → ``run.error`` kind ``provider_error``
"""

from __future__ import annotations

import json
import sys
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, List, Optional
from urllib.parse import unquote, urlsplit

import pytest

AGENT: Dict[str, Any] = {
    "name": "assistant",
    "description": "Mock agent for SDK tests",
    "model": "mock-1",
    "provider": "mock",
    "tools": ["calculator", "send_email", "now"],
}

TEAMS: List[Dict[str, Any]] = [
    {
        "name": "editorial",
        "description": "content team",
        "leader": "assistant",
        "members": ["researcher", "writer"],
        "process": "hierarchical",
    }
]

WORKFLOWS: List[Dict[str, Any]] = [
    {"name": "pipeline", "description": "two-step DAG", "steps": ["research", "write"]}
]


class _QuietServer(ThreadingHTTPServer):
    """Ignore client-disconnect noise (the Go server handles aborted
    streams via request context cancellation, so tests should too)."""

    def handle_error(self, request: Any, client_address: Any) -> None:
        exc = sys.exc_info()[1]
        # ConnectionError covers reset/aborted/broken-pipe on both POSIX
        # and Windows (WinError 10053/10054) when a client aborts a stream.
        if isinstance(exc, ConnectionError):
            return
        super().handle_error(request, client_address)


def _uid(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex[:8]}"


class MockErnestHandler(BaseHTTPRequestHandler):
    """Stateful handler mirroring web/scripts/mock-server.mjs."""

    sessions: Dict[str, Dict[str, Any]] = {}
    # approval id -> session id (mirrors agent.registerApproval in Go).
    approval_to_session: Dict[str, str] = {}
    # run id -> {"runId", "spans", "metrics"} (mirrors server.traces).
    traces: Dict[str, Dict[str, Any]] = {}

    def log_message(self, *args: Any) -> None:
        pass

    # -- plumbing -------------------------------------------------------

    def _read_body(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length).decode("utf-8")
        return json.loads(raw) if raw else {}

    def _json(self, status: int, body: Any) -> None:
        payload = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
        self.wfile.flush()
        # Mirror the Go server: the response ends when the handler returns.
        self.close_connection = True

    def _sse(self, events: List[dict]) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        for ev in events:
            self.wfile.write(f"data: {json.dumps(ev)}\n\n".encode("utf-8"))
            self.wfile.flush()
        # Stream end = connection close (same as the Go server's
        # streamEvents returning): clients read frames until EOF.
        self.close_connection = True

    # -- routes ---------------------------------------------------------

    def _path(self) -> str:
        """Path without query string (e.g. ``/api/sessions`` for
        ``/api/sessions?agent=assistant``)."""
        return urlsplit(self.path).path

    def do_GET(self) -> None:  # noqa: N802 (http.server API)
        path = self._path()
        if path == "/healthz":
            self._json(200, {"status": "ok", "agents": 1})
        elif path == "/api/agents":
            self._json(200, [AGENT])
        elif path == "/api/teams":
            self._json(200, TEAMS)
        elif path == "/api/workflows":
            self._json(200, WORKFLOWS)
        elif path == "/api/sessions":
            out = [
                {
                    "id": sid,
                    "agentName": s["agentName"],
                    "messages": len(s["messages"]),
                    "pendingApprovals": len(s["pendingApprovals"]),
                    "updatedAt": s["updatedAt"],
                }
                for sid, s in self.sessions.items()
            ]
            self._json(200, out)
        elif path.startswith("/api/sessions/"):
            sid = unquote(path[len("/api/sessions/") :])
            s = self.sessions.get(sid)
            if s is None:
                self._json(404, {"error": f"session {sid} not found"})
            else:
                self._json(200, s)
        elif path.startswith("/api/runs/"):
            head, tail = path[len("/api/runs/") :], ""
            if "/" in head:
                head, tail = head.split("/", 1)
            if tail == "trace":
                run_id = unquote(head)
                t = self.traces.get(run_id)
                if t is None:
                    self._json(404, {"error": f"unknown run {run_id}"})
                else:
                    self._json(200, t)
            else:
                self._json(404, {"error": f"no route: GET {self.path}"})
        else:
            self._json(404, {"error": f"no route: GET {self.path}"})

    def do_DELETE(self) -> None:  # noqa: N802
        path = self._path()
        if path.startswith("/api/sessions/"):
            sid = unquote(path[len("/api/sessions/") :])
            self.sessions.pop(sid, None)
            # Mutate the CLASS-level map in place: assigning via ``self.``
            # would create an instance attribute that shadows it, so
            # deletes would never be visible to other handler instances.
            MockErnestHandler.approval_to_session = {
                k: v for k, v in MockErnestHandler.approval_to_session.items() if v != sid
            }
            self._json(200, {"deleted": sid})
        else:
            self._json(404, {"error": f"no route: DELETE {self.path}"})

    def do_POST(self) -> None:  # noqa: N802
        body = self._read_body()
        path = self._path()
        if path == "/api/chat":
            if body.get("agent") != AGENT["name"]:
                self._json(404, {"error": f"unknown agent {body.get('agent')}"})
                return
            if not body.get("input"):
                self._json(400, {"error": "input is required"})
                return
            self._sse(self._chat_events(body, resume=None))
        elif path.startswith("/api/teams/") and path.endswith("/run"):
            name = unquote(path[len("/api/teams/") : -len("/run")])
            if not any(t["name"] == name for t in TEAMS):
                self._json(404, {"error": f"unknown team {name}"})
                return
            if not body.get("input"):
                self._json(400, {"error": "input is required"})
                return
            self._sse(self._team_events(name, body.get("input", "")))
        elif path.startswith("/api/workflows/") and path.endswith("/run"):
            name = unquote(path[len("/api/workflows/") : -len("/run")])
            if not any(w["name"] == name for w in WORKFLOWS):
                self._json(404, {"error": f"unknown workflow {name}"})
                return
            if not body.get("input"):
                self._json(400, {"error": "input is required"})
                return
            self._sse(self._workflow_events(name, body.get("input", "")))
        elif self._path() == "/api/approve":
            approval_id = body.get("approvalId", "")
            if approval_id not in self.approval_to_session:
                self._json(404, {"error": f"unknown approval {approval_id}"})
                return
            resume = {
                "id": approval_id,
                "approved": bool(body.get("approved", True)),
                "note": body.get("note", ""),
            }
            session_id = self.approval_to_session[approval_id]
            self._sse(self._chat_events({"agent": AGENT["name"], "sessionId": session_id}, resume=resume))
        else:
            self._json(404, {"error": f"no route: POST {self.path}"})

    # -- team/workflow flows --------------------------------------------

    def _team_events(self, name: str, input_text: str) -> List[dict]:
        run_id = _uid("run")
        now = _now()
        events: List[dict] = [
            {"type": "run.start", "runId": run_id, "agent": name, "data": {"input": input_text}},
            {"type": "delegate.start", "runId": run_id, "agent": "researcher", "data": {"task": input_text}},
            {"type": "delegate.end", "runId": run_id, "agent": "researcher", "data": {"task": input_text, "output": "findings"}},
            {"type": "run.complete", "runId": run_id, "agent": name, "result": {
                "runId": run_id,
                "status": "completed",
                "output": "synthesised answer",
                "durationMs": 210,
                "metadata": {"team": name, "process": "hierarchical", "members": 2},
            }},
        ]
        return events

    def _workflow_events(self, name: str, input_text: str) -> List[dict]:
        run_id = _uid("run")
        now = _now()
        return [
            {"type": "run.start", "runId": run_id, "agent": name, "data": {"input": input_text}},
            {"type": "step.start", "runId": run_id, "agent": name, "step": "research"},
            {"type": "step.end", "runId": run_id, "agent": name, "step": "research"},
            {"type": "step.start", "runId": run_id, "agent": name, "step": "write"},
            {"type": "step.end", "runId": run_id, "agent": name, "step": "write"},
            {"type": "run.complete", "runId": run_id, "agent": name, "result": {
                "runId": run_id,
                "status": "completed",
                "output": '{"input": "' + input_text + '", "research": "r", "write": "w"}',
                "durationMs": 310,
                "metadata": {"workflow": name, "steps": 2},
            }},
        ]

    # -- chat flow ------------------------------------------------------

    def _chat_events(self, body: dict, resume: Optional[dict]) -> List[dict]:
        run_id = _uid("run")
        agent_name = AGENT["name"]
        session_id = body.get("sessionId") or _uid("session")
        session = self.sessions.get(session_id)
        if session is None:
            session = {
                "id": session_id,
                "agentName": agent_name,
                "messages": [],
                "pendingApprovals": [],
                "pendingCalls": [],
                "createdAt": _now(),
                "updatedAt": _now(),
            }
            self.sessions[session_id] = session

        now = _now()
        if resume is None:
            session["messages"].append(
                {"role": "user", "content": body.get("input", ""), "createdAt": now}
            )

        # Error paths for typed-exception tests.
        failure_kind = {
            "boom": "agent_error",
            "tool-boom": "tool_error",
            "provider-boom": "provider_error",
        }.get(body.get("input", ""))
        if failure_kind and resume is None:
            error = f"{failure_kind}: simulated failure for input {body['input']!r}"
            events = [
                {"type": "run.start", "runId": run_id, "agent": agent_name, "data": {"input": body.get("input", "")}},
                {"type": "run.error", "runId": run_id, "agent": agent_name, "error": error},
                {
                    "type": "run.complete",
                    "runId": run_id,
                    "agent": agent_name,
                    "result": {
                        "runId": run_id,
                        "status": "failed",
                        "output": "",
                        "messages": session["messages"],
                        "error": error,
                        "durationMs": 120,
                        "metadata": {"agent": agent_name, "iterations": 1, "sessionId": session_id},
                    },
                },
            ]
            self.traces[run_id] = self._trace_for(run_id, events, "failed", 120)
            return events

        # Trace/metrics demo path for SDK tests: emits trace.span +
        # run.metrics and completes without an approval pause.
        if body.get("input") == "trace-me" and resume is None:
            span = {
                "id": _uid("span"),
                "runId": run_id,
                "name": "llm",
                "kind": "llm",
                "status": "ok",
                "startedAt": now,
                "durationMs": 210,
                "tokens": {"inputTokens": 88, "outputTokens": 42},
            }
            metrics = {
                "iterations": 1,
                "tokens": {"inputTokens": 88, "outputTokens": 42},
                "costCents": 0.03,
                "durationMs": 210,
                "status": "completed",
            }
            events = [
                {"type": "run.start", "runId": run_id, "agent": agent_name, "data": {"input": "trace-me"}},
                {"type": "message.delta", "runId": run_id, "agent": agent_name, "delta": "Traced"},
                {
                    "type": "message.complete",
                    "runId": run_id,
                    "agent": agent_name,
                    "message": {"role": "assistant", "content": "Traced run", "createdAt": now},
                },
                {"type": "trace.span", "runId": run_id, "agent": agent_name, "span": span},
                {"type": "run.metrics", "runId": run_id, "agent": agent_name, "metrics": metrics},
                {
                    "type": "run.complete",
                    "runId": run_id,
                    "agent": agent_name,
                    "result": {
                        "runId": run_id,
                        "status": "completed",
                        "output": "Traced run",
                        "messages": session["messages"],
                        "durationMs": 210,
                        "usage": {"inputTokens": 88, "outputTokens": 42},
                        "metadata": {"agent": agent_name, "iterations": 1, "sessionId": session_id},
                    },
                },
            ]
            self.traces[run_id] = {"runId": run_id, "spans": [span], "metrics": metrics}
            return events

        events: List[dict] = [
            {"type": "run.start", "runId": run_id, "agent": agent_name, "data": {"input": body.get("input", "")}}
        ]
        reply = (
            "Done — I sent the email. The tool executed successfully."
            if resume and resume["approved"]
            else "Sure — here is the result, and I can email it to you if you approve."
        )
        # Two token chunks for delta streaming.
        mid = len(reply) // 2
        events.append({"type": "message.delta", "runId": run_id, "agent": agent_name, "delta": reply[:mid]})
        events.append({"type": "message.delta", "runId": run_id, "agent": agent_name, "delta": reply[mid:]})
        events.append(
            {
                "type": "message.complete",
                "runId": run_id,
                "agent": agent_name,
                "message": {"role": "assistant", "content": reply, "createdAt": now},
            }
        )

        if resume is None:
            call = {
                "id": _uid("call"),
                "name": "send_email",
                "arguments": {"to": "team@example.com", "subject": "Ernest demo", "body": "Automated summary"},
            }
            approval = {
                "id": _uid("ap"),
                "runId": run_id,
                "agentName": agent_name,
                "action": "send_email",
                "summary": 'Send an email to team@example.com with subject "Ernest demo"?',
                "context": {"to": "team@example.com", "subject": "Ernest demo"},
                "status": "pending",
                "createdAt": now,
            }
            session["pendingApprovals"].append(approval)
            session["pendingCalls"].append({"approvalId": approval["id"], "call": call})
            self.approval_to_session[approval["id"]] = session_id
            events.append({"type": "tool.call", "runId": run_id, "agent": agent_name, "toolCall": call})
            events.append({"type": "approval.requested", "runId": run_id, "agent": agent_name, "approval": approval})
            events.append(
                {
                    "type": "run.complete",
                    "runId": run_id,
                    "agent": agent_name,
                    "result": {
                        "runId": run_id,
                        "status": "awaiting_approval",
                        "output": reply,
                        "messages": session["messages"],
                        "approvals": session["pendingApprovals"],
                        "durationMs": 640,
                        "metadata": {"agent": agent_name, "iterations": 1, "sessionId": session_id},
                    },
                }
            )
        else:
            ap = next((a for a in session["pendingApprovals"] if a["id"] == resume["id"]), None)
            if ap is not None:
                ap["status"] = "approved" if resume["approved"] else "rejected"
                ap["note"] = resume["note"]
                ap["resolvedAt"] = _now()
                events.append({"type": "approval.resolved", "runId": run_id, "agent": agent_name, "approval": ap})
            session["pendingApprovals"] = [a for a in session["pendingApprovals"] if a["id"] != resume["id"]]
            blocked = next((c for c in session["pendingCalls"] if c["approvalId"] == resume["id"]), None)
            session["pendingCalls"] = [c for c in session["pendingCalls"] if c["approvalId"] != resume["id"]]

            if blocked:
                tool_result = (
                    {"id": blocked["call"]["id"], "name": "send_email", "content": {"ok": True, "to": "team@example.com"}}
                    if resume["approved"]
                    else {
                        "id": blocked["call"]["id"],
                        "name": "send_email",
                        "content": {},
                        "error": "tool call rejected by human",
                        "approvalRequired": True,
                    }
                )
                events.append({"type": "tool.result", "runId": run_id, "agent": agent_name, "toolResult": tool_result})
                session["messages"].append(
                    {
                        "role": "tool",
                        "name": "send_email",
                        "toolCallID": tool_result["id"],
                        "content": json.dumps(tool_result),
                        "createdAt": _now(),
                    }
                )

            events.append(
                {
                    "type": "run.complete",
                    "runId": run_id,
                    "agent": agent_name,
                    "result": {
                        "runId": run_id,
                        "status": "completed",
                        "output": reply,
                        "messages": session["messages"],
                        "durationMs": 980,
                        "usage": {"inputTokens": 412, "outputTokens": 96},
                        "metadata": {"agent": agent_name, "iterations": 2, "sessionId": session_id},
                    },
                }
            )
        self.traces[run_id] = self._trace_for(run_id, events, "completed", 980)
        return events

    def _trace_for(self, run_id: str, events: List[dict], status: str, duration_ms: int) -> Dict[str, Any]:
        """Build a server-style trace payload from a run's event list."""
        spans = [
            {
                "id": _uid("span"),
                "runId": run_id,
                "name": "llm",
                "kind": "llm",
                "status": "ok" if status != "failed" else "error",
                "startedAt": _now(),
                "durationMs": duration_ms,
                "tokens": {"inputTokens": 400, "outputTokens": 90},
            }
        ]
        for ev in events:
            if ev.get("type") == "tool.call":
                call = ev.get("toolCall") or {}
                spans.append(
                    {
                        "id": _uid("span"),
                        "runId": run_id,
                        "name": f"tool:{call.get('name', '?')}",
                        "kind": "tool",
                        "status": "ok",
                        "startedAt": _now(),
                        "durationMs": 180,
                        "input": call.get("arguments"),
                    }
                )
        return {
            "runId": run_id,
            "spans": spans,
            "metrics": {
                "iterations": len(spans),
                "tokens": {"inputTokens": 400, "outputTokens": 90},
                "costCents": 0.05,
                "durationMs": duration_ms,
                "status": status,
            },
        }


def _now() -> str:
    from datetime import datetime, timezone

    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


@pytest.fixture(scope="session")
def mock_server() -> ThreadingHTTPServer:
    """Session-scoped mock backend on 127.0.0.1 with a random free port."""
    server = _QuietServer(("127.0.0.1", 0), MockErnestHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield server
    server.shutdown()
    server.server_close()


@pytest.fixture(autouse=True)
def _clean_state(mock_server: ThreadingHTTPServer) -> None:
    """Isolate server state between tests."""
    MockErnestHandler.sessions.clear()
    MockErnestHandler.approval_to_session.clear()
    MockErnestHandler.traces.clear()
    yield


@pytest.fixture()
def base_url(mock_server: ThreadingHTTPServer) -> str:
    host, port = mock_server.server_address
    return f"http://{host}:{port}"


@pytest.fixture(scope="session")
def ernest_bin() -> str:
    """Path to the built ernest binary (repo root), or skip the test.

    CI and local dev build it first: ``go build -o ernest ./cmd/ernest``.
    Tests that shell out to the real engine require it; everything else
    runs without it.
    """
    repo = Path(__file__).resolve().parents[2]
    for candidate in (repo / "ernest.exe", repo / "ernest"):
        if candidate.is_file():
            return str(candidate)
    pytest.skip("ernest binary not built (go build -o ernest ./cmd/ernest)")
