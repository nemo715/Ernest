"""ernest — Python SDK for the ernest multi-agent framework (Go core).

Sync + async clients for the HTTP API with SSE streaming and typed HITL
approvals. The wire format mirrors ``internal/core/events.go`` +
``types.go`` (also mirrored in ``web/lib/types.ts``).

Usage::

    from ernest import Client

    client = Client("http://127.0.0.1:9090")
    for event in client.stream_chat("assistant", "send a summary email"):
        print(event.type, event.delta)

    result = client.chat("assistant", "hello")
    if result.awaiting_approval:
        approval = result.approvals[0]
        result = client.approve("assistant", approval.id, approved=True,
                                note="looks good")
"""

from .async_client import AsyncClient
from .client import Client, DEFAULT_BASE_URL
from .dsl import Agent, Crew, Guard, Task, Team
from .errors import (
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
    SSEProtocolError,
    ToolError,
    ToolNotFoundError,
    ValidationError,
    api_error,
    error_from_event,
)
from .types import (
    APPROVAL_APPROVED,
    APPROVAL_PENDING,
    APPROVAL_REJECTED,
    EVENT_APPROVAL_REQUESTED,
    EVENT_APPROVAL_RESOLVED,
    EVENT_DELEGATE_END,
    EVENT_DELEGATE_START,
    EVENT_MESSAGE_COMPLETE,
    EVENT_MESSAGE_DELTA,
    EVENT_RUN_COMPLETE,
    EVENT_RUN_ERROR,
    EVENT_RUN_START,
    EVENT_STEP_END,
    EVENT_STEP_START,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    EVENT_TRACE_SPAN,
    EVENT_RUN_METRICS,
    EVENT_TYPES,
    PART_TEXT,
    PART_TOOL_CALL,
    PART_TOOL_RESULT,
    ROLE_ASSISTANT,
    ROLE_SYSTEM,
    ROLE_TOOL,
    ROLE_USER,
    RUN_AWAITING_APPROVAL,
    RUN_COMPLETED,
    RUN_FAILED,
    RUN_INTERRUPTED,
    AgentInfo,
    ApprovalRequest,
    ContentPart,
    Message,
    RunEvent,
    RunResult,
    RunTrace,
    Session,
    SessionInfo,
    TeamInfo,
    ToolCall,
    ToolResult,
    Usage,
    TraceSpan,
    RunMetrics,
    WorkflowInfo,
)

__version__ = "0.2.0"

__all__ = [
    "Client",
    "AsyncClient",
    "DEFAULT_BASE_URL",
    # Authoring DSL (compiles to ernest.json; runs on the Go engine).
    "Agent",
    "Team",
    "Task",
    "Guard",
    "Crew",
    "RunEvent",
    "RunResult",
    "RunTrace",
    "TraceSpan",
    "RunMetrics",
    "Message",
    "ContentPart",
    "ToolCall",
    "ToolResult",
    "ApprovalRequest",
    "Usage",
    "AgentInfo",
    "TeamInfo",
    "WorkflowInfo",
    "Session",
    "SessionInfo",
    "ErnestError",
    "APIError",
    "BadRequestError",
    "NotFoundError",
    "RateLimitError",
    "ServerError",
    "RunError",
    "AgentError",
    "ProviderError",
    "ToolError",
    "ToolNotFoundError",
    "ValidationError",
    "KnowledgeError",
    "MemoryError",
    "MCPError",
    "RunInterrupted",
    "RunTimeout",
    "SSEProtocolError",
    "api_error",
    "error_from_event",
    "EVENT_TYPES",
]
