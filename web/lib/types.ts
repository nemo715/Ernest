// TypeScript mirror of the ernest wire format (see internal/core/events.go
// and internal/core/types.go). The Go server is the single source of truth;
// keep these in sync when the Go JSON tags change.

export type EventType =
  | "run.start"
  | "message.delta"
  | "message.complete"
  | "tool.call"
  | "tool.result"
  | "approval.requested"
  | "approval.resolved"
  | "delegate.start"
  | "delegate.end"
  | "step.start"
  | "step.end"
  | "trace.span"
  | "run.metrics"
  | "run.complete"
  | "run.error";

export type Role = "system" | "user" | "assistant" | "tool";

export interface ToolCall {
  id: string;
  name: string;
  /** Raw JSON object of the call arguments. */
  arguments: unknown;
}

export interface ToolResult {
  id: string;
  name: string;
  /** JSON-encoded result payload. */
  content?: unknown;
  error?: string;
  approvalRequired?: boolean;
}

export interface ContentPart {
  type: string; // "text" | "tool_call" | "tool_result"
  text?: string;
  toolCall?: ToolCall;
  toolResult?: ToolResult;
}

export interface Message {
  role: Role;
  content?: string;
  parts?: ContentPart[];
  toolCalls?: ToolCall[];
  /** Tool name for role === "tool". */
  name?: string;
  /** Tool call id for role === "tool". */
  toolCallID?: string;
  createdAt: string;
}

export interface ApprovalRequest {
  id: string;
  runId: string;
  agentName: string;
  action: string;
  summary: string;
  context?: Record<string, unknown>;
  status: "pending" | "approved" | "rejected";
  note?: string;
  createdAt: string;
  resolvedAt?: string | null;
}

export interface Usage {
  inputTokens: number;
  outputTokens: number;
}

export interface TraceSpan {
  id: string;
  runId: string;
  parent?: string;
  /** e.g. llm, tool:send_email, approval, step:research */
  name: string;
  /** llm | tool | approval | step */
  kind: string;
  /** ok | error | blocked | cancelled */
  status: string;
  startedAt: string;
  durationMs: number;
  input?: unknown;
  output?: unknown;
  tokens?: Usage;
}

export interface RunMetrics {
  iterations: number;
  tokens?: Usage;
  costCents: number;
  durationMs: number;
  status: string;
}

export interface RunResult {
  runId: string;
  status: string; // completed | failed | interrupted | awaiting_approval
  output: string;
  messages: Message[];
  approvals?: ApprovalRequest[];
  usage?: Usage;
  error?: string;
  durationMs: number;
  metadata?: Record<string, unknown>;
}

export interface RunEvent {
  type: EventType;
  runId: string;
  agent?: string;
  delta?: string; // message.delta
  message?: Message; // message.complete
  toolCall?: ToolCall; // tool.call
  toolResult?: ToolResult; // tool.result
  approval?: ApprovalRequest; // approval.requested / approval.resolved
  step?: string; // step.start / step.end
  span?: TraceSpan; // trace.span
  metrics?: RunMetrics; // run.metrics
  result?: RunResult; // run.complete
  error?: string; // run.error
  data?: unknown;
}

// ---------------------------------------------------------------------------
// WebSocket transport (GET /ws/chat, internal/server/ws.go)
// ---------------------------------------------------------------------------

/** Client → server message on the /ws/chat connection. */
export type WSClientMessage =
  | { type: "chat"; agent: string; input: string; sessionId?: string; userId?: string }
  | { type: "steer"; input: string }
  | { type: "interrupt" }
  | { type: "approve"; agent: string; approvalId: string; approved: boolean; note?: string }
  | { type: "ping" };

/** Server → client control frame (RunEvents arrive as plain events). */
export type WSServerFrame =
  | { type: "ready"; agents: string[] }
  | { type: "pong" }
  | { type: "ack"; action: string }
  | { type: "error"; error: string };

// ---------------------------------------------------------------------------
// HTTP API (internal/server/server.go)
// ---------------------------------------------------------------------------

export interface AgentInfo {
  name: string;
  description: string;
  model: string;
  provider: string;
  tools: string[];
}

export interface ChatRequest {
  agent: string;
  input: string;
  sessionId?: string;
  userId?: string;
  temperature?: number;
  maxIterations?: number;
  skipMemory?: boolean;
}

export interface ApproveRequest {
  agent: string;
  approvalId: string;
  approved: boolean;
  note?: string;
}

export interface SessionInfo {
  id: string;
  agentName: string;
  userId?: string;
  messages: number;
  pendingApprovals: number;
  updatedAt: string;
}

export interface ApprovalDecision {
  approvalId: string;
  approved: boolean;
  note?: string;
}

export interface Session {
  id: string;
  agentName: string;
  userId?: string;
  messages: Message[];
  metadata?: Record<string, string>;
  pendingApprovals?: ApprovalRequest[];
  resolvedApprovals?: Record<string, ApprovalDecision>;
  pendingCalls?: { approvalId: string; call: ToolCall }[];
  toolCache?: Record<string, unknown>;
  createdAt: string;
  updatedAt: string;
}
