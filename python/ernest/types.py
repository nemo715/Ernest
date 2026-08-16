"""Typed mirror of the ernest wire format.

JSON keys follow the Go structs in ``internal/core/events.go`` and
``internal/core/types.go`` (camelCase) and the TypeScript mirror in
``web/lib/types.ts``. The Go server is the single source of truth — do
not rename JSON keys here without updating all three.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

# Stable event types (wire values — do not rename).
EVENT_RUN_START = "run.start"
EVENT_MESSAGE_DELTA = "message.delta"
EVENT_MESSAGE_COMPLETE = "message.complete"
EVENT_TOOL_CALL = "tool.call"
EVENT_TOOL_RESULT = "tool.result"
EVENT_APPROVAL_REQUESTED = "approval.requested"
EVENT_APPROVAL_RESOLVED = "approval.resolved"
EVENT_DELEGATE_START = "delegate.start"
EVENT_DELEGATE_END = "delegate.end"
EVENT_STEP_START = "step.start"
EVENT_STEP_END = "step.end"
EVENT_TRACE_SPAN = "trace.span"
EVENT_RUN_METRICS = "run.metrics"
EVENT_RUN_COMPLETE = "run.complete"
EVENT_RUN_ERROR = "run.error"

EVENT_TYPES = frozenset(
    [
        EVENT_RUN_START,
        EVENT_MESSAGE_DELTA,
        EVENT_MESSAGE_COMPLETE,
        EVENT_TOOL_CALL,
        EVENT_TOOL_RESULT,
        EVENT_APPROVAL_REQUESTED,
        EVENT_APPROVAL_RESOLVED,
        EVENT_DELEGATE_START,
        EVENT_DELEGATE_END,
        EVENT_STEP_START,
        EVENT_STEP_END,
        EVENT_TRACE_SPAN,
        EVENT_RUN_METRICS,
        EVENT_RUN_COMPLETE,
        EVENT_RUN_ERROR,
    ]
)

# Run statuses (wire values — do not rename).
RUN_COMPLETED = "completed"
RUN_FAILED = "failed"
RUN_INTERRUPTED = "interrupted"
RUN_AWAITING_APPROVAL = "awaiting_approval"

# Message roles.
ROLE_SYSTEM = "system"
ROLE_USER = "user"
ROLE_ASSISTANT = "assistant"
ROLE_TOOL = "tool"

# Content part types.
PART_TEXT = "text"
PART_TOOL_CALL = "tool_call"
PART_TOOL_RESULT = "tool_result"

# Approval statuses.
APPROVAL_PENDING = "pending"
APPROVAL_APPROVED = "approved"
APPROVAL_REJECTED = "rejected"


def _lst(cls: Any, items: Any) -> List[Any]:
    """Convert a list of dicts into dataclasses (skips non-dict entries)."""
    if not items:
        return []
    out = []
    for item in items:
        if isinstance(item, dict):
            parsed = cls.from_dict(item)
            if parsed is not None:
                out.append(parsed)
    return out


@dataclass
class ToolCall:
    """A request by the model to execute a tool."""

    id: str = ""
    name: str = ""
    arguments: Any = None  # raw JSON object of the call arguments

    @classmethod
    def from_dict(cls, d: Any) -> Optional["ToolCall"]:
        if not isinstance(d, dict):
            return None
        return cls(id=d.get("id", ""), name=d.get("name", ""), arguments=d.get("arguments"))


@dataclass
class ToolResult:
    """The outcome of a tool execution."""

    id: str = ""
    name: str = ""
    content: Any = None  # JSON-encoded result payload
    error: str = ""
    approval_required: bool = False

    @classmethod
    def from_dict(cls, d: Any) -> Optional["ToolResult"]:
        if not isinstance(d, dict):
            return None
        return cls(
            id=d.get("id", ""),
            name=d.get("name", ""),
            content=d.get("content"),
            error=d.get("error", "") or "",
            approval_required=bool(d.get("approvalRequired", False)),
        )


@dataclass
class ContentPart:
    """A single typed fragment of a message (text | tool_call | tool_result)."""

    type: str = ""
    text: str = ""
    tool_call: Optional[ToolCall] = None
    tool_result: Optional[ToolResult] = None

    @classmethod
    def from_dict(cls, d: Any) -> Optional["ContentPart"]:
        if not isinstance(d, dict):
            return None
        return cls(
            type=d.get("type", ""),
            text=d.get("text", "") or "",
            tool_call=ToolCall.from_dict(d.get("toolCall")),
            tool_result=ToolResult.from_dict(d.get("toolResult")),
        )


@dataclass
class Message:
    """A single turn in a conversation."""

    role: str = ""
    content: str = ""
    parts: List[ContentPart] = field(default_factory=list)
    tool_calls: List[ToolCall] = field(default_factory=list)
    name: str = ""  # tool name for role="tool"
    tool_call_id: str = ""  # tool call id for role="tool"
    created_at: str = ""  # RFC3339

    @classmethod
    def from_dict(cls, d: Any) -> Optional["Message"]:
        if not isinstance(d, dict):
            return None
        return cls(
            role=d.get("role", ""),
            content=d.get("content", "") or "",
            parts=_lst(ContentPart, d.get("parts")),
            tool_calls=_lst(ToolCall, d.get("toolCalls")),
            name=d.get("name", "") or "",
            tool_call_id=d.get("toolCallID", "") or "",
            created_at=d.get("createdAt", "") or "",
        )

    def text(self) -> str:
        """Plain-text content (mirrors Go ``Message.Text()``)."""
        if self.content:
            return self.content
        return "".join(p.text for p in self.parts if p.type == PART_TEXT)

    def has_tool_calls(self) -> bool:
        return bool(self.tool_calls)


@dataclass
class Usage:
    """Token consumption when the provider reports it."""

    input_tokens: int = 0
    output_tokens: int = 0

    @classmethod
    def from_dict(cls, d: Any) -> Optional["Usage"]:
        if not isinstance(d, dict):
            return None
        return cls(input_tokens=d.get("inputTokens", 0) or 0, output_tokens=d.get("outputTokens", 0) or 0)


@dataclass
class ApprovalRequest:
    """An in-flight human-in-the-loop request."""

    id: str = ""
    run_id: str = ""
    agent_name: str = ""
    action: str = ""  # e.g. "send_email"
    summary: str = ""  # human-readable description
    context: Dict[str, Any] = field(default_factory=dict)
    status: str = APPROVAL_PENDING  # pending | approved | rejected
    note: str = ""
    created_at: str = ""  # RFC3339
    resolved_at: Optional[str] = None  # RFC3339

    @classmethod
    def from_dict(cls, d: Any) -> Optional["ApprovalRequest"]:
        if not isinstance(d, dict):
            return None
        return cls(
            id=d.get("id", ""),
            run_id=d.get("runId", ""),
            agent_name=d.get("agentName", ""),
            action=d.get("action", ""),
            summary=d.get("summary", ""),
            context=d.get("context") or {},
            status=d.get("status", APPROVAL_PENDING),
            note=d.get("note", "") or "",
            created_at=d.get("createdAt", "") or "",
            resolved_at=d.get("resolvedAt"),
        )


@dataclass
class RunResult:
    """The outcome of an agent/team/workflow run."""

    run_id: str = ""
    status: str = ""
    output: str = ""
    messages: List[Message] = field(default_factory=list)
    approvals: List[ApprovalRequest] = field(default_factory=list)
    usage: Optional[Usage] = None
    error: str = ""
    duration_ms: int = 0
    metadata: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Any) -> Optional["RunResult"]:
        if not isinstance(d, dict):
            return None
        return cls(
            run_id=d.get("runId", ""),
            status=d.get("status", ""),
            output=d.get("output", "") or "",
            messages=_lst(Message, d.get("messages")),
            approvals=_lst(ApprovalRequest, d.get("approvals")),
            usage=Usage.from_dict(d.get("usage")),
            error=d.get("error", "") or "",
            duration_ms=d.get("durationMs", 0) or 0,
            metadata=d.get("metadata") or {},
        )

    @property
    def completed(self) -> bool:
        return self.status == RUN_COMPLETED

    @property
    def awaiting_approval(self) -> bool:
        return self.status == RUN_AWAITING_APPROVAL


@dataclass
class TraceSpan:
    """One instrumented operation inside a run (trace.span event).

    Kinds: ``llm`` | ``tool`` | ``approval`` | ``step``; status:
    ``ok`` | ``error`` | ``blocked`` | ``cancelled``.
    """

    id: str = ""
    run_id: str = ""
    parent: str = ""
    name: str = ""  # e.g. llm, tool:send_email, approval, step:research
    kind: str = ""
    status: str = ""
    started_at: str = ""  # RFC3339
    duration_ms: int = 0
    input: Any = None  # raw JSON input payload
    output: Any = None  # raw JSON output payload
    tokens: Optional[Usage] = None

    @classmethod
    def from_dict(cls, d: Any) -> Optional["TraceSpan"]:
        if not isinstance(d, dict):
            return None
        return cls(
            id=d.get("id", ""),
            run_id=d.get("runId", "") or "",
            parent=d.get("parent", "") or "",
            name=d.get("name", "") or "",
            kind=d.get("kind", "") or "",
            status=d.get("status", "") or "",
            started_at=d.get("startedAt", "") or "",
            duration_ms=d.get("durationMs", 0) or 0,
            input=d.get("input"),
            output=d.get("output"),
            tokens=Usage.from_dict(d.get("tokens")),
        )


@dataclass
class RunMetrics:
    """Live run summary emitted as a run.metrics event."""

    iterations: int = 0
    tokens: Optional[Usage] = None
    cost_cents: float = 0.0
    duration_ms: int = 0
    status: str = ""

    @classmethod
    def from_dict(cls, d: Any) -> Optional["RunMetrics"]:
        if not isinstance(d, dict):
            return None
        return cls(
            iterations=d.get("iterations", 0) or 0,
            tokens=Usage.from_dict(d.get("tokens")),
            cost_cents=d.get("costCents", 0.0) or 0.0,
            duration_ms=d.get("durationMs", 0) or 0,
            status=d.get("status", "") or "",
        )


@dataclass
class RunEvent:
    """A single event emitted during a run (stable wire format)."""

    type: str = ""
    run_id: str = ""
    agent: str = ""
    delta: str = ""  # message.delta
    message: Optional[Message] = None  # message.complete
    tool_call: Optional[ToolCall] = None  # tool.call
    tool_result: Optional[ToolResult] = None  # tool.result
    approval: Optional[ApprovalRequest] = None  # approval.requested / resolved
    step: str = ""  # step.start / step.end (workflow step name)
    span: Optional[TraceSpan] = None  # trace.span
    metrics: Optional[RunMetrics] = None  # run.metrics
    result: Optional[RunResult] = None  # run.complete
    error: str = ""  # run.error
    data: Any = None

    @classmethod
    def from_dict(cls, d: Any) -> Optional["RunEvent"]:
        if not isinstance(d, dict):
            return None
        return cls(
            type=d.get("type", ""),
            run_id=d.get("runId", "") or "",
            agent=d.get("agent", "") or "",
            delta=d.get("delta", "") or "",
            message=Message.from_dict(d.get("message")),
            tool_call=ToolCall.from_dict(d.get("toolCall")),
            tool_result=ToolResult.from_dict(d.get("toolResult")),
            approval=ApprovalRequest.from_dict(d.get("approval")),
            step=d.get("step", "") or "",
            span=TraceSpan.from_dict(d.get("span")),
            metrics=RunMetrics.from_dict(d.get("metrics")),
            result=RunResult.from_dict(d.get("result")),
            error=d.get("error", "") or "",
            data=d.get("data"),
        )


@dataclass
class AgentInfo:
    """Agent metadata served by GET /api/agents."""

    name: str = ""
    description: str = ""
    model: str = ""
    provider: str = ""
    tools: List[str] = field(default_factory=list)

    @classmethod
    def from_dict(cls, d: Any) -> Optional["AgentInfo"]:
        if not isinstance(d, dict):
            return None
        return cls(
            name=d.get("name", ""),
            description=d.get("description", "") or "",
            model=d.get("model", "") or "",
            provider=d.get("provider", "") or "",
            tools=list(d.get("tools") or []),
        )


@dataclass
class TeamInfo:
    """Team metadata served by GET /api/teams."""

    name: str = ""
    description: str = ""
    leader: str = ""
    members: List[str] = field(default_factory=list)
    process: str = "hierarchical"

    @classmethod
    def from_dict(cls, d: Any) -> Optional["TeamInfo"]:
        if not isinstance(d, dict):
            return None
        return cls(
            name=d.get("name", ""),
            description=d.get("description", "") or "",
            leader=d.get("leader", "") or "",
            members=list(d.get("members") or []),
            process=d.get("process", "hierarchical") or "hierarchical",
        )


@dataclass
class WorkflowInfo:
    """Workflow metadata served by GET /api/workflows."""

    name: str = ""
    description: str = ""
    steps: List[str] = field(default_factory=list)

    @classmethod
    def from_dict(cls, d: Any) -> Optional["WorkflowInfo"]:
        if not isinstance(d, dict):
            return None
        return cls(
            name=d.get("name", ""),
            description=d.get("description", "") or "",
            steps=list(d.get("steps") or []),
        )


@dataclass
class SessionInfo:
    """Session summary served by GET /api/sessions."""

    id: str = ""
    agent_name: str = ""
    user_id: str = ""
    messages: int = 0
    pending_approvals: int = 0
    updated_at: str = ""  # RFC3339

    @classmethod
    def from_dict(cls, d: Any) -> Optional["SessionInfo"]:
        if not isinstance(d, dict):
            return None
        return cls(
            id=d.get("id", ""),
            agent_name=d.get("agentName", ""),
            user_id=d.get("userId", "") or "",
            messages=d.get("messages", 0) or 0,
            pending_approvals=d.get("pendingApprovals", 0) or 0,
            updated_at=d.get("updatedAt", "") or "",
        )


@dataclass
class RunTrace:
    """Instrumented trace of a finished run: GET /api/runs/{id}/trace."""

    run_id: str = ""
    spans: List[TraceSpan] = field(default_factory=list)
    metrics: Optional[RunMetrics] = None

    @classmethod
    def from_dict(cls, d: Any) -> Optional["RunTrace"]:
        if not isinstance(d, dict):
            return None
        return cls(
            run_id=d.get("runId", "") or "",
            spans=_lst(TraceSpan, d.get("spans")),
            metrics=RunMetrics.from_dict(d.get("metrics")),
        )


@dataclass
class Session:
    """Full session served by GET /api/sessions/{id}."""

    id: str = ""
    agent_name: str = ""
    user_id: str = ""
    messages: List[Message] = field(default_factory=list)
    metadata: Dict[str, str] = field(default_factory=dict)
    pending_approvals: List[ApprovalRequest] = field(default_factory=list)
    resolved_approvals: Dict[str, Any] = field(default_factory=dict)
    pending_calls: List[Any] = field(default_factory=list)
    tool_cache: Dict[str, Any] = field(default_factory=dict)
    created_at: str = ""  # RFC3339
    updated_at: str = ""  # RFC3339

    @classmethod
    def from_dict(cls, d: Any) -> Optional["Session"]:
        if not isinstance(d, dict):
            return None
        return cls(
            id=d.get("id", ""),
            agent_name=d.get("agentName", ""),
            user_id=d.get("userId", "") or "",
            messages=_lst(Message, d.get("messages")),
            metadata=d.get("metadata") or {},
            pending_approvals=_lst(ApprovalRequest, d.get("pendingApprovals")),
            resolved_approvals=d.get("resolvedApprovals") or {},
            pending_calls=d.get("pendingCalls") or [],
            tool_cache=d.get("toolCache") or {},
            created_at=d.get("createdAt", "") or "",
            updated_at=d.get("updatedAt", "") or "",
        )
