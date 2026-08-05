"""Integration tests for the sync :class:`ernest.Client` against the mock
backend (same SSE contract as web/scripts/mock-server.mjs)."""

from __future__ import annotations

from typing import List

import pytest

from ernest import (
    AgentError,
    BadRequestError,
    Client,
    NotFoundError,
    ProviderError,
    SSEProtocolError,
    ToolError,
)
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
    RunEvent,
)


def test_invalid_base_url() -> None:
    with pytest.raises(ValueError):
        Client("ftp://example.com")


def test_health(base_url: str) -> None:
    health = Client(base_url).health()
    assert health["status"] == "ok"
    assert health["agents"] == 1


def test_list_agents(base_url: str) -> None:
    agents = Client(base_url).list_agents()
    assert len(agents) == 1
    agent = agents[0]
    assert agent.name == "assistant"
    assert agent.provider == "mock"
    assert "send_email" in agent.tools


def test_stream_chat_event_sequence(base_url: str) -> None:
    types = [ev.type for ev in Client(base_url).stream_chat("assistant", "hello")]
    assert types == [
        EVENT_RUN_START,
        EVENT_MESSAGE_DELTA,
        EVENT_MESSAGE_DELTA,
        EVENT_MESSAGE_COMPLETE,
        EVENT_TOOL_CALL,
        EVENT_APPROVAL_REQUESTED,
        EVENT_RUN_COMPLETE,
    ]


def test_stream_deltas_join_to_output(base_url: str) -> None:
    client = Client(base_url)
    deltas = "".join(ev.delta for ev in client.stream_chat("assistant", "hello") if ev.type == EVENT_MESSAGE_DELTA)
    assert "email" in deltas


def test_run_start_event_payload(base_url: str) -> None:
    first = next(Client(base_url).stream_chat("assistant", "hello"))
    assert first.type == EVENT_RUN_START
    assert first.run_id
    assert first.agent == "assistant"
    assert first.data == {"input": "hello"}


def test_chat_returns_awaiting_approval_result(base_url: str) -> None:
    result = Client(base_url).chat("assistant", "hello")
    assert result.awaiting_approval
    assert result.status == RUN_AWAITING_APPROVAL
    assert result.output
    assert result.approvals
    approval = result.approvals[0]
    assert approval.action == "send_email"
    assert approval.status == "pending"
    assert approval.agent_name == "assistant"
    assert approval.run_id == result.run_id
    assert approval.summary and approval.context.get("to") == "team@example.com"


def test_message_helpers(base_url: str) -> None:
    result = Client(base_url).chat("assistant", "hello")
    user_msg = next(m for m in result.messages if m.role == "user")
    assert user_msg.text() == "hello"


def test_sessions_crud(base_url: str) -> None:
    client = Client(base_url)
    sid = "session_sync_1"
    client.chat("assistant", "hello", session_id=sid)

    infos = client.list_sessions()
    assert any(i.id == sid and i.agent_name == "assistant" and i.messages == 1 for i in infos)

    session = client.get_session(sid)
    assert session.id == sid
    assert session.messages[0].role == "user"
    assert session.messages[0].content == "hello"
    assert session.pending_approvals[0].status == "pending"

    assert client.delete_session(sid) == sid
    assert not any(i.id == sid for i in client.list_sessions())


def test_list_sessions_filtered_by_agent(base_url: str) -> None:
    client = Client(base_url)
    client.chat("assistant", "hello", session_id="filtered_1")
    assert any(i.id == "filtered_1" for i in client.list_sessions(agent="assistant"))


def test_get_missing_session_raises(base_url: str) -> None:
    with pytest.raises(NotFoundError) as exc_info:
        Client(base_url).get_session("missing_session")
    assert exc_info.value.status == 404


def test_approve_after_session_delete_raises(base_url: str) -> None:
    """Deleting a session must invalidate its pending approvals — the
    mock's approval→session map is shared state and must be cleaned too."""
    client = Client(base_url)
    sid = "session_deleted_1"
    result = client.chat("assistant", "hello", session_id=sid)
    approval_id = result.approvals[0].id

    assert client.delete_session(sid) == sid

    with pytest.raises(NotFoundError) as exc_info:
        client.approve("assistant", approval_id, True)
    assert exc_info.value.status == 404
    assert "unknown approval" in exc_info.value.message
    # The deleted session must not be resurrected by the failed approve.
    with pytest.raises(NotFoundError):
        client.get_session(sid)


def test_approve_flow_approves(base_url: str) -> None:
    client = Client(base_url)
    events = list(client.stream_chat("assistant", "hello"))
    call = next(ev.tool_call for ev in events if ev.type == EVENT_TOOL_CALL)
    approval = next(ev.approval for ev in events if ev.type == EVENT_APPROVAL_REQUESTED)

    resumed = list(client.stream_approve("assistant", approval.id, True, note="looks good"))
    resolved = next(ev.approval for ev in resumed if ev.type == EVENT_APPROVAL_RESOLVED)
    assert resolved.status == "approved"
    assert resolved.note == "looks good"
    assert resolved.id == approval.id

    # Resume replays the blocked call with the SAME id (UI/`findToolOwner` contract).
    tool_result = next(ev.tool_result for ev in resumed if ev.type == EVENT_TOOL_RESULT)
    assert tool_result.id == call.id
    assert tool_result.name == "send_email"
    assert tool_result.content == {"ok": True, "to": "team@example.com"}

    final = next(ev.result for ev in resumed if ev.type == EVENT_RUN_COMPLETE)
    assert final.completed
    assert final.usage is not None
    assert final.usage.input_tokens == 412
    assert final.usage.output_tokens == 96
    assert final.duration_ms > 0


def test_approve_convenience_returns_result(base_url: str) -> None:
    client = Client(base_url)
    result = client.chat("assistant", "hello")
    approval_id = result.approvals[0].id
    resumed = client.approve("assistant", approval_id, True)
    assert resumed.completed
    assert "email" in resumed.output


def test_approve_rejects(base_url: str) -> None:
    client = Client(base_url)
    result = client.chat("assistant", "hello")
    approval_id = result.approvals[0].id
    resumed = list(client.stream_approve("assistant", approval_id, False, note="no thanks"))
    resolved = next(ev.approval for ev in resumed if ev.type == EVENT_APPROVAL_RESOLVED)
    assert resolved.status == "rejected"
    tool_result = next(ev.tool_result for ev in resumed if ev.type == EVENT_TOOL_RESULT)
    assert tool_result.error == "tool call rejected by human"
    assert tool_result.approval_required is True


def test_run_error_raises_typed_exception(base_url: str) -> None:
    client = Client(base_url)
    with pytest.raises(AgentError) as exc_info:
        list(client.stream_chat("assistant", "boom"))
    exc = exc_info.value
    assert exc.message == "simulated failure for input 'boom'"
    assert exc.run_id
    assert exc.agent == "assistant"


@pytest.mark.parametrize(
    "input,expected",
    [("tool-boom", ToolError), ("provider-boom", ProviderError)],
)
def test_run_error_kind_mapping_over_wire(base_url: str, input: str, expected: type) -> None:
    with pytest.raises(expected):
        list(Client(base_url).stream_chat("assistant", input))


def test_chat_convenience_raises_run_error(base_url: str) -> None:
    with pytest.raises(AgentError):
        Client(base_url).chat("assistant", "boom")


def test_unknown_agent_returns_404(base_url: str) -> None:
    with pytest.raises(NotFoundError) as exc_info:
        Client(base_url).chat("ghost", "hi")
    assert exc_info.value.status == 404
    assert "unknown agent ghost" in exc_info.value.message


def test_missing_input_returns_400(base_url: str) -> None:
    with pytest.raises(BadRequestError):
        Client(base_url).chat("assistant", "")


def test_unknown_approval_returns_404(base_url: str) -> None:
    with pytest.raises(NotFoundError) as exc_info:
        Client(base_url).approve("assistant", "no_such_approval", True)
    assert exc_info.value.status == 404


def test_session_id_url_quoting(base_url: str) -> None:
    client = Client(base_url)
    sid = "session/with slashes & spaces"
    client.chat("assistant", "hello", session_id=sid)
    session = client.get_session(sid)
    assert session.id == sid
    assert client.delete_session(sid) == sid


def test_stream_parses_trace_and_metrics_events(base_url: str) -> None:
    """trace.span + run.metrics events parse into typed fields."""
    events = list(Client(base_url).stream_chat("assistant", "trace-me"))
    types = [ev.type for ev in events]
    assert EVENT_TRACE_SPAN in types
    assert EVENT_RUN_METRICS in types

    span_ev = next(ev for ev in events if ev.type == EVENT_TRACE_SPAN)
    assert span_ev.span is not None
    assert span_ev.span.kind == "llm"
    assert span_ev.span.status == "ok"
    assert span_ev.span.duration_ms > 0
    assert span_ev.span.tokens is not None
    assert span_ev.span.tokens.input_tokens == 88

    metrics_ev = next(ev for ev in events if ev.type == EVENT_RUN_METRICS)
    assert metrics_ev.metrics is not None
    assert metrics_ev.metrics.status == "completed"
    assert metrics_ev.metrics.cost_cents == 0.03
    assert metrics_ev.metrics.iterations == 1


def test_get_run_trace(base_url: str) -> None:
    client = Client(base_url)
    result = client.chat("assistant", "trace-me")
    assert result.completed

    trace = client.get_run_trace(result.run_id)
    assert trace.run_id == result.run_id
    assert trace.spans
    span = trace.spans[0]
    assert span.kind == "llm"
    assert span.status == "ok"
    assert span.name == "llm"
    assert span.duration_ms == 210
    assert span.tokens is not None
    assert trace.metrics is not None
    assert trace.metrics.cost_cents > 0
    assert trace.metrics.status == "completed"


def test_get_run_trace_includes_tool_spans(base_url: str) -> None:
    """The mock traces every run, including tool calls, so a normal
    chat's trace contains a tool:send_email span."""
    client = Client(base_url)
    result = client.chat("assistant", "hello")
    trace = client.get_run_trace(result.run_id)
    names = [s.name for s in trace.spans]
    assert "llm" in names
    assert "tool:send_email" in names
    tool = next(s for s in trace.spans if s.kind == "tool")
    assert tool.input == {"to": "team@example.com", "subject": "Ernest demo", "body": "Automated summary"}


def test_get_missing_run_trace_raises(base_url: str) -> None:
    with pytest.raises(NotFoundError) as exc_info:
        Client(base_url).get_run_trace("run_does_not_exist")
    assert exc_info.value.status == 404
    assert "unknown run" in exc_info.value.message


def test_events_typed_and_stable(base_url: str) -> None:
    """Every yielded event is a typed RunEvent (no raw dicts leak)."""
    events: List[RunEvent] = list(Client(base_url).stream_chat("assistant", "hello"))
    assert all(isinstance(ev, RunEvent) for ev in events)
    assert all(ev.run_id == events[0].run_id for ev in events)
    assert events[-1].type == EVENT_RUN_COMPLETE


def test_stream_without_complete_raises_protocol_error() -> None:
    """A stream ending without run.complete must fail loudly."""

    class FakeClient(Client):
        def _stream(self, method, path, body):  # type: ignore[override]
            yield RunEvent(type=EVENT_RUN_START, run_id="r", agent="a")

    fake = FakeClient("http://127.0.0.1:1")
    with pytest.raises(SSEProtocolError):
        fake.chat("assistant", "hello")
