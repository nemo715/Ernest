"""Async client for the ernest HTTP API (stdlib asyncio only, no deps).

API surface mirrors :class:`ernest.client.Client` — the same routes, the
same wire format, the same typed exceptions — but every method is a
coroutine and the streaming methods are async generators.
"""

from __future__ import annotations

import asyncio
import json
import urllib.parse
from typing import Any, AsyncIterator, Dict, List, Optional, Tuple

from .client import DEFAULT_BASE_URL
from .errors import APIError, SSEProtocolError, api_error, error_from_event
from .sse import aiter_sse_json
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


class AsyncClient:
    """Async ernest API client.

    Args:
        base_url: ernest server origin, e.g. ``http://127.0.0.1:9090``.
        timeout: seconds for the connection and each read.
    """

    def __init__(self, base_url: str = DEFAULT_BASE_URL, timeout: float = 60.0):
        parts = urllib.parse.urlsplit(base_url)
        if parts.scheme not in ("http",) or not parts.hostname:
            raise ValueError(f"invalid base_url: {base_url!r} (http://host:port expected)")
        self._parts = parts
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    # ------------------------------------------------------------------
    # HTTP plumbing
    # ------------------------------------------------------------------

    async def _open(
        self, method: str, path: str, body: Optional[dict] = None, query: Optional[Dict[str, str]] = None
    ) -> Tuple[asyncio.StreamReader, asyncio.StreamWriter, int]:
        parts = self._parts
        host = parts.hostname
        port = parts.port or (443 if parts.scheme == "https" else 80)
        target = (parts.path.rstrip("/") + path) or "/"
        if query:
            target += "?" + urllib.parse.urlencode(query)

        data = None
        headers = [
            f"Host: {host}:{port}",
            "Accept: application/json",
            "Connection: close",
            "User-Agent: ernest-python/0.1",
        ]
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers.append("Content-Type: application/json")
        request = (
            f"{method} {target} HTTP/1.1\r\n"
            + "\r\n".join(headers)
            + f"\r\nContent-Length: {len(data) if data is not None else 0}\r\n\r\n"
        ).encode("utf-8")

        reader, writer = await asyncio.wait_for(
            asyncio.open_connection(host, port), timeout=self.timeout
        )
        try:
            writer.write(request + (data or b""))
            await writer.drain()

            status_line = await asyncio.wait_for(reader.readline(), timeout=self.timeout)
            status = int(status_line.split(b" ", 2)[1])

            while True:  # consume headers
                line = await asyncio.wait_for(reader.readline(), timeout=self.timeout)
                if line in (b"\r\n", b"\n", b""):
                    break

            if status >= 400:
                message = await _read_until_eof(reader, self.timeout)
                raise api_error(status, _parse_error_body(message, status))
        except Exception:
            writer.close()
            await _close(writer)
            raise
        return reader, writer, status

    async def _json(self, method: str, path: str, body: Optional[dict] = None, query: Optional[Dict[str, str]] = None) -> Any:
        reader, writer, _ = await self._open(method, path, body=body, query=query)
        try:
            raw = await _read_until_eof(reader, self.timeout)
            return json.loads(raw) if raw else None
        finally:
            writer.close()
            await _close(writer)

    # ------------------------------------------------------------------
    # Streaming runs
    # ------------------------------------------------------------------

    async def stream_chat(
        self,
        agent: str,
        input: str,
        session_id: Optional[str] = None,
        user_id: Optional[str] = None,
        temperature: Optional[float] = None,
        max_iterations: Optional[int] = None,
        skip_memory: bool = False,
    ) -> AsyncIterator[RunEvent]:
        """Run an agent; yields :class:`RunEvent` frames as they stream."""
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
        async for event in self._stream("POST", "/api/chat", body):
            yield event

    async def chat(
        self,
        agent: str,
        input: str,
        session_id: Optional[str] = None,
        user_id: Optional[str] = None,
        temperature: Optional[float] = None,
        max_iterations: Optional[int] = None,
        skip_memory: bool = False,
    ) -> RunResult:
        """Run an agent and return the final :class:`RunResult`."""
        return await _collect_result(
            self.stream_chat(agent, input, session_id, user_id, temperature, max_iterations, skip_memory)
        )

    async def stream_approve(
        self, agent: str, approval_id: str, approved: bool, note: Optional[str] = None
    ) -> AsyncIterator[RunEvent]:
        """Resolve a HITL approval; yields the resumed run's events."""
        body: Dict[str, Any] = {"agent": agent, "approvalId": approval_id, "approved": bool(approved)}
        if note:
            body["note"] = note
        async for event in self._stream("POST", "/api/approve", body):
            yield event

    async def approve(self, agent: str, approval_id: str, approved: bool, note: Optional[str] = None) -> RunResult:
        """Resolve a HITL approval and return the resumed run's result."""
        return await _collect_result(self.stream_approve(agent, approval_id, approved, note))

    async def _stream(self, method: str, path: str, body: dict) -> AsyncIterator[RunEvent]:
        reader, writer, _ = await self._open(method, path, body=body)

        async def _read_line() -> Optional[bytes]:
            return await asyncio.wait_for(reader.readline(), timeout=self.timeout)

        try:
            async for payload in aiter_sse_json(_read_line):
                event = RunEvent.from_dict(payload)
                if event is None:
                    raise SSEProtocolError(f"non-object SSE payload: {payload!r}")
                if event.type == EVENT_RUN_ERROR:
                    raise error_from_event(event)
                yield event
        finally:
            writer.close()
            await _close(writer)

    # ------------------------------------------------------------------
    # Discovery + sessions
    # ------------------------------------------------------------------

    async def health(self) -> Dict[str, Any]:
        """GET /healthz — server liveness + agent count."""
        return await self._json("GET", "/healthz")

    async def list_agents(self) -> List[AgentInfo]:
        """GET /api/agents — available agents."""
        data = await self._json("GET", "/api/agents") or []
        return [a for a in (AgentInfo.from_dict(d) for d in data) if a is not None]

    # ------------------------------------------------------------------
    # Teams + workflows
    # ------------------------------------------------------------------

    async def list_teams(self) -> List[TeamInfo]:
        """GET /api/teams — config-declared teams."""
        data = await self._json("GET", "/api/teams") or []
        return [t for t in (TeamInfo.from_dict(d) for d in data) if t is not None]

    async def stream_team(self, name: str, input: str) -> AsyncIterator[RunEvent]:
        """Run a config-declared team; yields its events."""
        async for event in self._stream(
            "POST", f"/api/teams/{urllib.parse.quote(name, safe='')}/run", {"input": input}
        ):
            yield event

    async def run_team(self, name: str, input: str) -> RunResult:
        """Run a config-declared team and return its final result."""
        return await _collect_result(self.stream_team(name, input))

    async def list_workflows(self) -> List[WorkflowInfo]:
        """GET /api/workflows — config-declared workflows."""
        data = await self._json("GET", "/api/workflows") or []
        return [w for w in (WorkflowInfo.from_dict(d) for d in data) if w is not None]

    async def stream_workflow(self, name: str, input: str) -> AsyncIterator[RunEvent]:
        """Run a config-declared workflow; yields its events."""
        async for event in self._stream(
            "POST", f"/api/workflows/{urllib.parse.quote(name, safe='')}/run", {"input": input}
        ):
            yield event

    async def run_workflow(self, name: str, input: str) -> RunResult:
        """Run a config-declared workflow and return its final result."""
        return await _collect_result(self.stream_workflow(name, input))

    async def list_sessions(self, agent: Optional[str] = None) -> List[SessionInfo]:
        """GET /api/sessions — session summaries (optionally filtered)."""
        query = {"agent": agent} if agent else None
        data = await self._json("GET", "/api/sessions", query=query) or []
        return [s for s in (SessionInfo.from_dict(d) for d in data) if s is not None]

    async def get_session(self, session_id: str) -> Session:
        """GET /api/sessions/{id} — full session with message history."""
        data = await self._json("GET", f"/api/sessions/{urllib.parse.quote(session_id, safe='')}")
        session = Session.from_dict(data)
        if session is None:
            raise SSEProtocolError(f"unexpected session payload: {data!r}")
        return session

    async def delete_session(self, session_id: str) -> str:
        """DELETE /api/sessions/{id} — remove a session; returns its id."""
        data = await self._json("DELETE", f"/api/sessions/{urllib.parse.quote(session_id, safe='')}") or {}
        return data.get("deleted", session_id)

    async def get_run_trace(self, run_id: str) -> RunTrace:
        """GET /api/runs/{id}/trace — instrumented spans of a finished run.

        Every completed run (chat, approve resume, workflow, team run) is
        traced server-side; this returns its ``trace.span`` list plus the
        final ``run.metrics`` summary.
        """
        data = await self._json("GET", f"/api/runs/{urllib.parse.quote(run_id, safe='')}/trace")
        trace = RunTrace.from_dict(data)
        if trace is None:
            raise SSEProtocolError(f"unexpected trace payload: {data!r}")
        return trace


async def _collect_result(events: AsyncIterator[RunEvent]) -> RunResult:
    """Consume an event stream and return its ``run.complete`` result."""
    result = None
    async for event in events:
        if event.type == EVENT_RUN_COMPLETE and event.result is not None:
            result = event.result
    if result is None:
        raise SSEProtocolError("stream ended without run.complete")
    return result


async def _read_until_eof(reader: asyncio.StreamReader, timeout: float) -> bytes:
    """Read the response body to EOF (safe with ``Connection: close``)."""
    chunks = bytearray()
    while True:
        try:
            chunk = await asyncio.wait_for(reader.read(65536), timeout=timeout)
        except asyncio.TimeoutError:
            break
        if not chunk:
            break
        chunks.extend(chunk)
    return bytes(chunks)


async def _close(writer: asyncio.StreamWriter) -> None:
    try:
        await writer.wait_closed()
    except (ConnectionError, OSError):
        pass


def _parse_error_body(raw: bytes, status: int) -> str:
    """Extract ``{"error": msg}`` (or raw text) from an error body."""
    if not raw:
        return f"HTTP {status}"
    try:
        payload = json.loads(raw.decode("utf-8"))
        if isinstance(payload, dict) and payload.get("error"):
            return str(payload["error"])
    except (ValueError, TypeError):
        pass
    return raw.decode("utf-8", "replace")[:500]
