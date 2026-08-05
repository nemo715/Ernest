// Package core defines the fundamental types shared across ernest:
// messages, tool calls, run events, errors and the JSON schema engine.
package core

import (
	"encoding/json"
	"time"
)

// Role identifies who produced a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentPart is a single typed fragment of a message.
// Only one of Text, ToolCall or ToolResult is set.
type ContentPart struct {
	Type    string `json:"type"` // "text" | "tool_call" | "tool_result"
	Text    string `json:"text,omitempty"`
	ToolCall *ToolCall `json:"toolCall,omitempty"`
	ToolResult *ToolResult `json:"toolResult,omitempty"`
}

// ToolCall is a request by the model to execute a tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // raw JSON object
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`          // JSON-encoded result
	Error   string          `json:"error,omitempty"`  // set when the tool failed
	ApprovalRequired bool   `json:"approvalRequired,omitempty"`
}

// Message is a single turn in a conversation.
type Message struct {
	Role         Role          `json:"role"`
	Content      string        `json:"content,omitempty"`
	Parts        []ContentPart `json:"parts,omitempty"`
	ToolCalls    []ToolCall    `json:"toolCalls,omitempty"`
	Name         string        `json:"name,omitempty"` // tool name for role=tool
	ToolCallID   string        `json:"toolCallID,omitempty"` // tool call id for role=tool
	CreatedAt    time.Time     `json:"createdAt"`
}

// Text returns the plain-text content of the message.
func (m *Message) Text() string {
	if m.Content != "" {
		return m.Content
	}
	var sb []byte
	for _, p := range m.Parts {
		if p.Type == "text" {
			sb = append(sb, p.Text...)
		}
	}
	return string(sb)
}

// HasToolCalls reports whether the message contains tool calls.
func (m *Message) HasToolCalls() bool { return len(m.ToolCalls) > 0 }

// NewUserMessage builds a user message.
func NewUserMessage(content string) Message {
	return Message{Role: RoleUser, Content: content, CreatedAt: time.Now().UTC()}
}

// NewAssistantMessage builds an assistant message (optionally with tool calls).
func NewAssistantMessage(content string, calls []ToolCall) Message {
	return Message{Role: RoleAssistant, Content: content, ToolCalls: calls, CreatedAt: time.Now().UTC()}
}

// NewToolMessage builds a tool result message. id is the tool call id
// being answered; name is the tool's name.
func NewToolMessage(id, name string, content json.RawMessage) Message {
	return Message{Role: RoleTool, Name: name, ToolCallID: id, Content: string(content), CreatedAt: time.Now().UTC()}
}

// MessageFromJSON decodes a message (used by HTTP clients and persistence).
func MessageFromJSON(data []byte) (Message, error) {
	var m Message
	err := json.Unmarshal(data, &m)
	return m, err
}

// RunStatus describes the terminal state of a run.
type RunStatus string

const (
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusInterrupted RunStatus = "interrupted"       // cancelled or error
	RunStatusAwaitingApproval RunStatus = "awaiting_approval" // HITL pause
)

// RunResult is the outcome of an agent/team/workflow run.
type RunResult struct {
	RunID     string     `json:"runId"`
	Status    RunStatus  `json:"status"`
	Output    string     `json:"output"`
	Messages  []Message  `json:"messages"`
	Approvals []ApprovalRequest `json:"approvals,omitempty"`
	Usage     *Usage     `json:"usage,omitempty"`
	Error     string     `json:"error,omitempty"`
	DurationMS int64     `json:"durationMs"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Usage tracks token consumption when the provider reports it.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// ApprovalRequest is an in-flight human-in-the-loop request.
type ApprovalRequest struct {
	ID          string         `json:"id"`
	RunID       string         `json:"runId"`
	AgentName   string         `json:"agentName"`
	Action      string         `json:"action"`   // e.g. "send_email"
	Summary     string         `json:"summary"`  // human-readable description
	Context     map[string]any `json:"context,omitempty"`
	Status      string         `json:"status"`   // pending | approved | rejected
	Note        string         `json:"note,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	ResolvedAt  *time.Time     `json:"resolvedAt,omitempty"`
}

// ApprovalDecision is submitted when resuming an interrupted run.
type ApprovalDecision struct {
	ApprovalID string `json:"approvalId"`
	Approved   bool   `json:"approved"`
	Note       string `json:"note,omitempty"`
}
