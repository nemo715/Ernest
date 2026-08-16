"""Synchronous client for the ernest HTTP API (stdlib only).

Mirrors ``internal/server/server.go`` routes:

- ``GET  /healthz``
- ``GET  /api/agents``
- ``POST /api/chat`` — SSE stream of :class:`RunEvent`
- ``POST /api/approve`` — SSE stream of the resumed run
- ``GET  /api/sessions`` / ``GET /api/sessions/{id}`` / ``DELETE /api/sessions/{id}``

Streams raise a typed exception from :mod:`ernest.errors` when a
``run.error`` event arrives; non-2xx responses raise an :class:`APIError`
subclass.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, Iterator, List, Optional

from .errors import APIError, SSEProtocolError, api_error, error_from_event
from .sse import iter_sse_json
from .types import (
    EVENT_RUN_COMPLETE,
    EVENT_RUN_ERROR,
    AgentInfo,
    RunEvent,
    RunResult,
    RunTrace,
    Session,
    SessionInfo,
    TeamInfo,
    WorkflowInfo,
)

DEFAULT_BASE_URL = "http://127.0.0.1:9090"


class Client:
    """Sync ernest API client.

    Args:
        base_url: ernest server origin, e.g. ``http://127.0.0.1:9090``.
        timeout: seconds for each HTTP/read operation.
    """

    def __init__(self, base_url: str = DEFAULT_BASE_URL, timeout: float = 60.0):
        parts = urllib.parse.urlsplit(base_url)
        if parts.scheme not in ("http",) or not parts.hostname:
            raise ValueError(f"invalid base_url: {base_url!r} (http://host:port expected)")
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    # ------------------------------------------------------------------
    # HTTP plumbing
    # ------------------------------------------------------------------

    def _url(self, path: str, query: Optional[Dict[str, str]] = None) -> str:
        url = self.base_url + path
        if query:
            url += "?" + urllib.parse.urlencode(query)
        return url

    def _open(self, method: str, path: str, body: Optional[dict] = None, query: Optional[Dict[str, str]] = None):
        data = None
        headers: Dict[str, str] = {"Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            self._url(path, query), data=data, headers=headers, method=method
        )
        try:
            return urllib.request.urlopen(request, timeout=self.timeout)
        except urllib.error.HTTPError as exc:
            raise api_error(exc.code, _error_message(exc)) from exc
        except urllib.error.URLError as exc:
            raise APIError(0, f"connection error: {exc.reason}") from exc

    def _json(self, method: str, path: str, body: Optional[dict] = None, query: Optional[Dict[str, str]] = None) -> Any:
        with self._open(method, path, body=body, query=query) as response:
            raw = response.read().decode("utf-8")
            return json.loads(raw) if raw else None

    # ------------------------------------------------------------------
    # Streaming runs
    # ------------------------------------------------------------------

    def stream_chat(
        self,
        agent: str,
        input: str,
        session_id: Optional[str] = None,
        user_id: Optional[str] = None,
        temperature: Optional[float] = None,
        max_iterations: Optional[int] = None,
        skip_memory: bool = False,
    ) -> Iterator[RunEvent]:
        """Run an agent; yields :class:`RunEvent` frames as they stream.

        Raises a typed :class:`RunError` subclass if a ``run.error`` event
        arrives, or an :class:`APIError` subclass for non-2xx responses.
        """
        body: Dict[str, Any] = {"agent": agent, "input": input}
        if session_id:
            body["sessionId"] = session_id
        if user_id:
            body["userId"] = user_id
        if temperature is not None:
            body["temperature"] = temperature
        if max_iterations:
            body["maxIterations"] = max_iterations
        if skip_memory:
            body["skipMemory"] = True
        yield from self._stream("POST", "/api/chat", body)

    def chat(
        self,
        agent: str,
        input: str,
        session_id: Optional[str] = None,
        user_id: Optional[str] = None,
        temperature: Optional[float] = None,
        max_iterations: Optional[int] = None,
        skip_memory: bool = False,
    ) -> RunResult:
        """Run an agent and return the final :class:`RunResult`.

        Consumes the full stream. A run paused for HITL returns
        ``status == "awaiting_approval"`` (use :meth:`approve` to resume);
        a ``run.error`` raises the mapped typed exception.
        """
        result = _collect_result(self.stream_chat(agent, input, session_id, user_id, temperature, max_iterations, skip_memory))
        return result

    def stream_approve(
        self, agent: str, approval_id: str, approved: bool, note: Optional[str] = None
    ) -> Iterator[RunEvent]:
        """Resolve a HITL approval; yields the resumed run's events."""
        body: Dict[str, Any] = {"agent": agent, "approvalId": approval_id, "approved": bool(approved)}
        if note:
            body["note"] = note
        yield from self._stream("POST", "/api/approve", body)

    def approve(self, agent: str, approval_id: str, approved: bool, note: Optional[str] = None) -> RunResult:
        """Resolve a HITL approval and return the resumed run's result."""
        return _collect_result(self.stream_approve(agent, approval_id, approved, note))

    def _stream(self, method: str, path: str, body: dict) -> Iterator[RunEvent]:
        response = self._open(method, path, body=body)
        try:
            for payload in iter_sse_json(lambda: response.readline()):
                event = RunEvent.from_dict(payload)
                if event is None:
                    raise SSEProtocolError(f"non-object SSE payload: {payload!r}")
                if event.type == EVENT_RUN_ERROR:
                    raise error_from_event(event)
                yield event
        finally:
            response.close()

    # ------------------------------------------------------------------
    # Discovery + sessions
    # ------------------------------------------------------------------

    def health(self) -> Dict[str, Any]:
        """GET /healthz — server liveness + agent count."""
        return self._json("GET", "/healthz")

    def list_agents(self) -> List[AgentInfo]:
        """GET /api/agents — available agents."""
        data = self._json("GET", "/api/agents") or []
        return [a for a in (AgentInfo.from_dict(d) for d in data) if a is not None]

    # ------------------------------------------------------------------
    # Teams + workflows
    # ------------------------------------------------------------------

    def list_teams(self) -> List[TeamInfo]:
        """GET /api/teams — config-declared teams."""
        data = self._json("GET", "/api/teams") or []
        return [t for t in (TeamInfo.from_dict(d) for d in data) if t is not None]

    def stream_team(self, name: str, input: str) -> Iterator[RunEvent]:
        """Run a config-declared team; yields its events (delegate.start/
        delegate.end around member calls, then run.complete)."""
        yield from self._stream(
            "POST", f"/api/teams/{urllib.parse.quote(name, safe='')}/run", {"input": input}
        )

    def run_team(self, name: str, input: str) -> RunResult:
        """Run a config-declared team and return its final result."""
        return _collect_result(self.stream_team(name, input))

    def list_workflows(self) -> List[WorkflowInfo]:
        """GET /api/workflows — config-declared workflows."""
        data = self._json("GET", "/api/workflows") or []
        return [w for w in (WorkflowInfo.from_dict(d) for d in data) if w is not None]

    def stream_workflow(self, name: str, input: str) -> Iterator[RunEvent]:
        """Run a config-declared workflow; yields step.start/step.end
        events and the final run.complete (output = shared state JSON)."""
        yield from self._stream(
            "POST", f"/api/workflows/{urllib.parse.quote(name, safe='')}/run", {"input": input}
        )

    def run_workflow(self, name: str, input: str) -> RunResult:
        """Run a config-declared workflow and return its final result."""
        return _collect_result(self.stream_workflow(name, input))

    def list_sessions(self, agent: Optional[str] = None) -> List[SessionInfo]:
        """GET /api/sessions — session summaries (optionally filtered)."""
        query = {"agent": agent} if agent else None
        data = self._json("GET", "/api/sessions", query=query) or []
        return [s for s in (SessionInfo.from_dict(d) for d in data) if s is not None]

    def get_session(self, session_id: str) -> Session:
        """GET /api/sessions/{id} — full session with message history."""
        data = self._json("GET", f"/api/sessions/{urllib.parse.quote(session_id, safe='')}")
        session = Session.from_dict(data)
        if session is None:
            raise SSEProtocolError(f"unexpected session payload: {data!r}")
        return session

    def delete_session(self, session_id: str) -> str:
        """DELETE /api/sessions/{id} — remove a session; returns its id."""
        data = self._json("DELETE", f"/api/sessions/{urllib.parse.quote(session_id, safe='')}") or {}
        return data.get("deleted", session_id)

    def get_run_trace(self, run_id: str) -> RunTrace:
        """GET /api/runs/{id}/trace — instrumented spans of a finished run.

        Every completed run (chat, approve resume, workflow, team run) is
        traced server-side; this returns its ``trace.span`` list plus the
        final ``run.metrics`` summary.
        """
        data = self._json("GET", f"/api/runs/{urllib.parse.quote(run_id, safe='')}/trace")
        trace = RunTrace.from_dict(data)
        if trace is None:
            raise SSEProtocolError(f"unexpected trace payload: {data!r}")
        return trace


def _collect_result(events: Iterator[RunEvent]) -> RunResult:
    """Consume an event stream and return its ``run.complete`` result."""
    result = None
    for event in events:
        if event.type == EVENT_RUN_COMPLETE and event.result is not None:
            result = event.result
    if result is None:
        raise SSEProtocolError("stream ended without run.complete")
    return result


def _error_message(exc: urllib.error.HTTPError) -> str:
    """Extract ``{"error": msg}`` (or raw text) from an HTTP error body."""
    try:
        body = exc.read().decode("utf-8")
    except Exception:
        return "unknown server error"
    if not body:
        return f"HTTP {exc.code}"
    try:
        payload = json.loads(body)
        if isinstance(payload, dict) and payload.get("error"):
            return str(payload["error"])
    except (ValueError, TypeError):
        pass
    return body[:500]
