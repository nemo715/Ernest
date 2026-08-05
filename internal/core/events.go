package core

import (
	"encoding/json"
	"time"
)

// EventType is the discriminant of a run event (stable wire format).
type EventType string

const (
	EventRunStart         EventType = "run.start"
	EventMessageDelta     EventType = "message.delta"
	EventMessageComplete  EventType = "message.complete"
	EventToolCall         EventType = "tool.call"
	EventToolResult       EventType = "tool.result"
	EventApprovalRequest  EventType = "approval.requested"
	EventApprovalResolved EventType = "approval.resolved"
	EventDelegateStart    EventType = "delegate.start"
	EventDelegateEnd      EventType = "delegate.end"
	EventStepStart        EventType = "step.start"
	EventStepEnd          EventType = "step.end"
	EventTraceSpan        EventType = "trace.span"
	EventRunMetrics       EventType = "run.metrics"
	EventRunComplete      EventType = "run.complete"
	EventRunError         EventType = "run.error"
)

// TraceSpan is one instrumented operation inside a run: a model call,
// a tool execution, an approval pause or a workflow step. Spans are
// emitted as trace.span events (closed, with duration) and stored by the
// server for /api/runs/{id}/trace.
type TraceSpan struct {
	ID         string          `json:"id"`
	RunID      string          `json:"runId"`
	Parent     string          `json:"parent,omitempty"`
	Name       string          `json:"name"` // e.g. llm, tool:send_email, approval, step:research
	Kind       string          `json:"kind"` // llm | tool | approval | step
	Status     string          `json:"status"` // ok | error | blocked | cancelled
	StartedAt  time.Time       `json:"startedAt"`
	DurationMS int64           `json:"durationMs"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Tokens     *Usage          `json:"tokens,omitempty"`
}

// RunMetrics is the live run summary emitted as a run.metrics event:
// cost estimate, tokens, iterations and status.
type RunMetrics struct {
	Iterations int     `json:"iterations"`
	Tokens     *Usage  `json:"tokens,omitempty"`
	CostCents  float64 `json:"costCents"`
	DurationMS int64   `json:"durationMs"`
	Status     string  `json:"status"`
}

// RunEvent is a single event emitted during a run. It serialises to the
// stable wire format consumed by the CLI, the web UI and the Python SDK.
type RunEvent struct {
	Type    EventType       `json:"type"`
	RunID   string          `json:"runId"`
	Agent   string          `json:"agent,omitempty"`
	Delta   string          `json:"delta,omitempty"`   // message.delta
	Message *Message        `json:"message,omitempty"` // message.complete
	ToolCall *ToolCall      `json:"toolCall,omitempty"`
	ToolResult *ToolResult  `json:"toolResult,omitempty"`
	Approval *ApprovalRequest `json:"approval,omitempty"`
	Step     string         `json:"step,omitempty"` // workflow step name
	Span     *TraceSpan     `json:"span,omitempty"`
	Metrics  *RunMetrics    `json:"metrics,omitempty"`
	Result   *RunResult     `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Encode serialises the event to JSON (for SSE frames and SDK transport).
func (e RunEvent) Encode() []byte {
	data, err := json.Marshal(e)
	if err != nil {
		data, _ = json.Marshal(RunEvent{Type: EventRunError, Error: "event encode: " + err.Error()})
	}
	return data
}

// DecodeEvent parses a RunEvent from JSON.
func DecodeEvent(data []byte) (RunEvent, error) {
	var e RunEvent
	err := json.Unmarshal(data, &e)
	return e, err
}
