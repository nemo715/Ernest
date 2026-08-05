"""Typed exceptions for the ernest Python SDK.

``run.error`` events carry Go ``core.Error`` strings formatted as
``"<kind>: <message>"`` (see ``internal/core/errors.go``). The SDK maps
each kind to a typed exception so callers can handle provider failures,
tool failures, HITL rejects, timeouts and interrupts programmatically.

Non-2xx HTTP responses (``{"error": msg}``) map to :class:`APIError`
subclasses keyed by status code.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Type

# ---------------------------------------------------------------------------
# Base
# ---------------------------------------------------------------------------


class ErnestError(Exception):
    """Base class for all SDK errors."""


# ---------------------------------------------------------------------------
# HTTP API errors
# ---------------------------------------------------------------------------


class APIError(ErnestError):
    """A non-2xx response from the ernest HTTP API."""

    def __init__(self, status: int, message: str):
        super().__init__(f"API error {status}: {message}")
        self.status = status
        self.message = message


class BadRequestError(APIError):
    """HTTP 400 — malformed request (e.g. missing ``input``)."""


class NotFoundError(APIError):
    """HTTP 404 — unknown agent, approval or session."""


class RateLimitError(APIError):
    """HTTP 429 — provider rate limit."""


class ServerError(APIError):
    """HTTP 5xx — the ernest server failed."""


def api_error(status: int, message: str) -> APIError:
    """Build the APIError subclass matching an HTTP status code."""
    if status == 400:
        return BadRequestError(status, message)
    if status == 404:
        return NotFoundError(status, message)
    if status == 429:
        return RateLimitError(status, message)
    if status >= 500:
        return ServerError(status, message)
    return APIError(status, message)


# ---------------------------------------------------------------------------
# run.error events (mapped from Go core.Error kinds)
# ---------------------------------------------------------------------------


class RunError(ErnestError):
    """A ``run.error`` event arrived during a streamed run.

    Attributes:
        kind: the Go error kind (``agent_error`` by default).
        message: the error message (without the kind prefix).
        run_id: id of the failing run (may be empty).
        agent: agent that produced the error (may be empty).
    """

    kind = "agent_error"

    def __init__(self, message: str, run_id: str = "", agent: str = ""):
        super().__init__(message)
        self.message = message
        self.run_id = run_id
        self.agent = agent


class AgentError(RunError):
    """The agent runner itself failed (kind ``agent_error``)."""

    kind = "agent_error"


class ProviderError(RunError):
    """The LLM provider failed (kind ``provider_error``)."""

    kind = "provider_error"


class ToolError(RunError):
    """A tool execution failed (kind ``tool_error``)."""

    kind = "tool_error"


class ToolNotFoundError(RunError):
    """The model called a tool the agent does not have (``tool_not_found``)."""

    kind = "tool_not_found"


class ValidationError(RunError):
    """Input or configuration validation failed (``validation_error``)."""

    kind = "validation_error"


class KnowledgeError(RunError):
    """Knowledge base retrieval failed (``knowledge_error``)."""

    kind = "knowledge_error"


class MemoryError(RunError):
    """Session memory failed (``memory_error``)."""

    kind = "memory_error"


class MCPError(RunError):
    """An MCP tool server call failed (``mcp_error``)."""

    kind = "mcp_error"


class RunInterrupted(RunError):
    """The run was interrupted (kind ``interrupted``)."""

    kind = "interrupted"


class RunTimeout(RunError):
    """The run or a provider call timed out (kind ``timeout``)."""

    kind = "timeout"


class SSEProtocolError(ErnestError):
    """The SSE stream was malformed or ended without ``run.complete``."""


_KIND_TO_ERROR: Dict[str, Type[RunError]] = {
    cls.kind: cls
    for cls in (
        AgentError,
        ProviderError,
        ToolError,
        ToolNotFoundError,
        ValidationError,
        KnowledgeError,
        MemoryError,
        MCPError,
        RunInterrupted,
        RunTimeout,
    )
}


def error_from_event(event: Any) -> RunError:
    """Map a ``RunEvent(type="run.error")`` to a typed exception.

    The event's ``error`` string is expected to be a Go ``core.Error``
    serialization ``"<kind>: <message>"``. Unknown kinds fall back to
    :class:`RunError` with the original message preserved.
    """
    raw = (event.error or "").strip()
    kind, sep, message = raw.partition(": ")
    if not sep:
        kind, message = "", raw
    cls = _KIND_TO_ERROR.get(kind, RunError)
    if cls is RunError and kind:
        # Unknown kind prefix — keep the full raw string as the message.
        message = raw
    return cls(message=message, run_id=event.run_id or "", agent=event.agent or "")
