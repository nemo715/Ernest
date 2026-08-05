"""Integration tests for the async :class:`ernest.AsyncClient` against the
mock backend. Uses ``asyncio.run`` so no pytest plugins are required."""

from __future__ import annotations

import asyncio
from typing import Any, Callable, Optional

import pytest

from ernest import AgentError, AsyncClient, NotFoundError
from ernest.types import (
    EVENT_APPROVAL_REQUESTED,
    EVENT_APPROVAL_RESOLVED,
    EVENT_MESSAGE_COMPLETE,
    EVENT_MESSAGE_DELTA,
    EVENT_RUN_COMPLETE,
    EVENT_RUN_METRICS,
    EVENT_RUN_START,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    EVENT_TRACE_SPAN,
    RUN_AWAITING_APPROVAL,
)

T = Any


def _run(coro_factory: Callable[[], T]) -> T:
    return asyncio.run(coro_factory())


def test_async_health(base_url: str) -> None:
    async def main() -> dict:
        return await AsyncClient(base_url).health()

    health = _run(main)
    assert health["status"] == "ok"


def test_async_list_agents(base_url: str) -> None:
    async def main() -> list:
        return await AsyncClient(base_url).list_agents()

    agents = _run(main)
    assert agents[0].name == "assistant"
    assert "send_email" in agents[0].tools


def test_async_stream_chat_sequence(base_url: str) -> None:
    async def main() -> list:
        return [ev.type async for ev in AsyncClient(base_url).stream_chat("assistant", "hello")]

    types = _run(main)
    assert types == [
        EVENT_RUN_START,
        EVENT_MESSAGE_DELTA,
        EVENT_MESSAGE_DELTA,
        EVENT_MESSAGE_COMPLETE,
        EVENT_TOOL_CALL,
        EVENT_APPROVAL_REQUESTED,
        EVENT_RUN_COMPLETE,
    ]


def test_async_chat_result(base_url: str) -> None:
    async def main() -> Any:
        return await AsyncClient(base_url).chat("assistant", "hello")

    result = _run(main)
    assert result.status == RUN_AWAITING_APPROVAL
    assert result.approvals[0].action == "send_email"


def test_async_approve_flow(base_url: str) -> None:
    async def main() -> tuple:
        client = AsyncClient(base_url)
        events = [ev async for ev in client.stream_chat("assistant", "hello")]
        call = next(ev.tool_call for ev in events if ev.type == EVENT_TOOL_CALL)
        approval = next(ev.approval for ev in events if ev.type == EVENT_APPROVAL_REQUESTED)

        resumed = [ev async for ev in client.stream_approve("assistant", approval.id, True, note="go")]
        resolved = next(ev.approval for ev in resumed if ev.type == EVENT_APPROVAL_RESOLVED)
        tool_result = next(ev.tool_result for ev in resumed if ev.type == EVENT_TOOL_RESULT)
        final = next(ev.result for ev in resumed if ev.type == EVENT_RUN_COMPLETE)
        return resolved.status, tool_result.id == call.id, tool_result.content, final.completed, final.usage

    status, same_call_id, content, completed, usage = _run(main)
    assert status == "approved"
    assert same_call_id
    assert content == {"ok": True, "to": "team@example.com"}
    assert completed
    assert usage is not None and usage.input_tokens == 412


def test_async_approve_convenience(base_url: str) -> None:
    async def main() -> Any:
        client = AsyncClient(base_url)
        first = await client.chat("assistant", "hello")
        return await client.approve("assistant", first.approvals[0].id, False, note="skip")

    result = _run(main)
    assert result.completed


def test_async_run_error_raises(base_url: str) -> None:
    async def main() -> Optional[AgentError]:
        try:
            async for _ in AsyncClient(base_url).stream_chat("assistant", "boom"):
                pass
            return None
        except AgentError as exc:
            return exc

    exc = _run(main)
    assert exc is not None
    assert exc.message == "simulated failure for input 'boom'"
    assert exc.agent == "assistant"


def test_async_unknown_agent(base_url: str) -> None:
    async def main() -> Optional[NotFoundError]:
        try:
            await AsyncClient(base_url).chat("ghost", "hi")
            return None
        except NotFoundError as exc:
            return exc

    exc = _run(main)
    assert exc is not None
    assert exc.status == 404


def test_async_sessions_crud(base_url: str) -> None:
    async def main() -> tuple:
        client = AsyncClient(base_url)
        sid = "session_async_1"
        await client.chat("assistant", "hello", session_id=sid)
        ids = [i.id for i in await client.list_sessions()]
        session = await client.get_session(sid)
        deleted = await client.delete_session(sid)
        ids_after = [i.id for i in await client.list_sessions()]
        return sid, ids, session.messages[0].content, deleted, ids_after

    sid, ids, content, deleted, ids_after = _run(main)
    assert sid in ids
    assert content == "hello"
    assert deleted == sid
    assert sid not in ids_after


def test_async_stream_partial_consumption_closes_connection(base_url: str) -> None:
    """Aborting a stream early must not hang the next request."""

    async def main() -> Any:
        client = AsyncClient(base_url)
        stream = client.stream_chat("assistant", "hello")
        first = await anext(stream)
        await stream.aclose()
        # A fresh request must still work after the aborted stream.
        return await client.chat("assistant", "hello")

    result = _run(main)
    assert result.awaiting_approval


def test_async_stream_parses_trace_and_metrics_events(base_url: str) -> None:
    async def main() -> tuple:
        events = [ev async for ev in AsyncClient(base_url).stream_chat("assistant", "trace-me")]
        span_ev = next(ev for ev in events if ev.type == EVENT_TRACE_SPAN)
        metrics_ev = next(ev for ev in events if ev.type == EVENT_RUN_METRICS)
        return (
            span_ev.span.kind,
            span_ev.span.status,
            span_ev.span.duration_ms,
            span_ev.span.tokens.input_tokens if span_ev.span.tokens else None,
            metrics_ev.metrics.status,
            metrics_ev.metrics.cost_cents,
        )

    kind, status, duration_ms, input_tokens, m_status, cost = _run(main)
    assert kind == "llm"
    assert status == "ok"
    assert duration_ms > 0
    assert input_tokens == 88
    assert m_status == "completed"
    assert cost == 0.03


def test_async_get_run_trace(base_url: str) -> None:
    async def main() -> tuple:
        client = AsyncClient(base_url)
        result = await client.chat("assistant", "trace-me")
        trace = await client.get_run_trace(result.run_id)
        return (
            trace.run_id,
            [s.name for s in trace.spans],
            trace.metrics.status if trace.metrics else None,
            trace.metrics.cost_cents if trace.metrics else None,
        )

    run_id, names, m_status, cost = _run(main)
    assert names == ["llm"]
    assert m_status == "completed"
    assert cost > 0


def test_async_get_missing_run_trace_raises(base_url: str) -> None:
    async def main() -> Optional[NotFoundError]:
        try:
            await AsyncClient(base_url).get_run_trace("run_does_not_exist")
            return None
        except NotFoundError as exc:
            return exc

    exc = _run(main)
    assert exc is not None
    assert exc.status == 404
    assert "unknown run" in exc.message
