"""Unit tests for ernest.errors: run.error → typed exceptions, HTTP error mapping."""

from __future__ import annotations

import pytest

from ernest import (
    APIError,
    AgentError,
    BadRequestError,
    ErnestError,
    KnowledgeError,
    MCPError,
    MemoryError,
    NotFoundError,
    ProviderError,
    RateLimitError,
    RunError,
    RunInterrupted,
    RunTimeout,
    ServerError,
    ToolError,
    ToolNotFoundError,
    ValidationError,
    api_error,
    error_from_event,
)
from ernest.types import RunEvent

KIND_CASES = [
    ("agent_error: boom", AgentError),
    ("provider_error: upstream 503", ProviderError),
    ("tool_error: boom", ToolError),
    ("tool_not_found: unknown tool", ToolNotFoundError),
    ("validation_error: input required", ValidationError),
    ("knowledge_error: vector store down", KnowledgeError),
    ("memory_error: trim failed", MemoryError),
    ("mcp_error: server crashed", MCPError),
    ("interrupted: cancelled by user", RunInterrupted),
    ("timeout: provider slow", RunTimeout),
]


def _event(error: str, run_id: str = "run_1", agent: str = "assistant") -> RunEvent:
    return RunEvent(type="run.error", run_id=run_id, agent=agent, error=error)


@pytest.mark.parametrize("raw,cls", KIND_CASES)
def test_run_error_kind_mapping(raw: str, cls: type) -> None:
    exc = error_from_event(_event(raw))
    assert isinstance(exc, cls)
    assert isinstance(exc, RunError)
    assert exc.kind == cls.kind


def test_run_error_attributes() -> None:
    exc = error_from_event(_event("provider_error: upstream 503", run_id="run_9", agent="helper"))
    assert exc.run_id == "run_9"
    assert exc.agent == "helper"
    assert exc.message == "upstream 503"


def test_run_error_without_kind_prefix() -> None:
    exc = error_from_event(_event("plain failure"))
    assert isinstance(exc, RunError)
    assert exc.message == "plain failure"


def test_run_error_unknown_kind_keeps_raw_message() -> None:
    exc = error_from_event(_event("mystery_kind: something"))
    assert isinstance(exc, RunError)
    assert exc.message == "mystery_kind: something"


def test_run_error_empty() -> None:
    exc = error_from_event(_event(""))
    assert isinstance(exc, RunError)
    assert exc.message == ""


def test_error_hierarchy() -> None:
    assert issubclass(RunError, ErnestError)
    assert issubclass(APIError, ErnestError)
    for cls in (AgentError, ProviderError, ToolError, RunTimeout, RunInterrupted):
        assert issubclass(cls, RunError)
    for cls in (BadRequestError, NotFoundError, RateLimitError, ServerError):
        assert issubclass(cls, APIError)


def test_api_error_status_mapping() -> None:
    assert isinstance(api_error(400, "bad"), BadRequestError)
    assert isinstance(api_error(404, "nope"), NotFoundError)
    assert isinstance(api_error(429, "slow down"), RateLimitError)
    assert isinstance(api_error(500, "oops"), ServerError)
    assert isinstance(api_error(502, "oops"), ServerError)
    assert isinstance(api_error(418, "teapot"), APIError)
    assert not isinstance(api_error(418, "teapot"), BadRequestError)


def test_api_error_fields() -> None:
    exc = api_error(404, "unknown agent ghost")
    assert exc.status == 404
    assert exc.message == "unknown agent ghost"
    assert "404" in str(exc)
