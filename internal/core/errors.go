package core

import "fmt"

// Kind classifies an error for programmatic handling and API mapping.
type Kind string

const (
	KindAgent       Kind = "agent_error"
	KindProvider    Kind = "provider_error"
	KindTool        Kind = "tool_error"
	KindToolNotFound Kind = "tool_not_found"
	KindValidation  Kind = "validation_error"
	KindKnowledge   Kind = "knowledge_error"
	KindMemory      Kind = "memory_error"
	KindMCP         Kind = "mcp_error"
	KindInterrupt   Kind = "interrupted"
	KindTimeout     Kind = "timeout"
)

// Error is the typed error used across ernest. Always wrap unexpected
// failures with AsError so the kind is preserved over the wire.
type Error struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a typed ernest error.
func NewError(kind Kind, msg string, cause ...error) *Error {
	e := &Error{Kind: kind, Message: msg}
	if len(cause) > 0 {
		e.Cause = cause[0]
	}
	return e
}

// AsError coerces any error into *Error, preserving typed errors.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	if ee, ok := err.(*Error); ok {
		return ee
	}
	return NewError(KindAgent, err.Error(), err)
}

// KindOf returns the Kind of an error (Agent kind for unknown errors).
func KindOf(err error) Kind {
	if ee, ok := err.(*Error); ok {
		return ee.Kind
	}
	return KindAgent
}

// ApprovalRequiredError is returned by tools that need human-in-the-loop
// approval before executing. The agent run loop intercepts it, persists
// the run, and emits an approval.requested event.
type ApprovalRequiredError struct {
	Action  string         `json:"action"`
	Summary string         `json:"summary"`
	Context map[string]any `json:"context,omitempty"`
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("approval required: %s (%s)", e.Action, e.Summary)
}

// Retryable reports whether the error is worth retrying (provider 429/5xx,
// timeouts, network failures).
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	k := KindOf(err)
	return k == KindProvider || k == KindTimeout
}
