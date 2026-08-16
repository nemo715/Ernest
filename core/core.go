// Package core is the public API for the ernest wire format: events,
// results, tools. It forwards to the implementation in ernest/internal/core.
package core

import (
	"context"

	internal "github.com/nemo715/Ernest/internal/core"
	internalBrowser "github.com/nemo715/Ernest/internal/browser"
)

type (
	Tool             = internal.Tool
	ToolCall         = internal.ToolCall
	ToolResult       = internal.ToolResult
	ToolContext      = internal.ToolContext
	RunEvent         = internal.RunEvent
	RunResult        = internal.RunResult
	RunStatus        = internal.RunStatus
	RunMetrics       = internal.RunMetrics
	Usage            = internal.Usage
	Message          = internal.Message
	ApprovalRequest  = internal.ApprovalRequest
	ApprovalRequiredError = internal.ApprovalRequiredError
)

// NewTool registers a custom tool: the model sees {name, description} and
// calls it with JSON args of T; fn executes it (returns any JSON-marshalable
// result). Custom tools are how ernest apps gain new hands.
func NewTool[T any](name, description string, fn func(ctx context.Context, tc *ToolContext, args T) (any, error)) (*Tool, error) {
	return internal.NewTool(name, description, fn)
}

// MustTool is NewTool but panics on construction errors.
func MustTool[T any](name, description string, fn func(ctx context.Context, tc *ToolContext, args T) (any, error)) *Tool {
	return internal.MustTool(name, description, fn)
}

// BrowserTool drives a lazy shared Edge/Chrome via CDP (go-rod): navigate,
// read HTML, screenshot, evaluate JS. The browser only launches on first use.
var BrowserTool = internalBrowser.Tool

const (
	EventRunStart           = internal.EventRunStart
	EventMessageDelta       = internal.EventMessageDelta
	EventMessageComplete    = internal.EventMessageComplete
	EventToolCall           = internal.EventToolCall
	EventToolResult         = internal.EventToolResult
	EventApprovalRequest  = internal.EventApprovalRequest
	EventApprovalResolved = internal.EventApprovalResolved
	EventStepStart          = internal.EventStepStart
	EventStepEnd            = internal.EventStepEnd
	EventDelegateStart      = internal.EventDelegateStart
	EventDelegateEnd        = internal.EventDelegateEnd
	EventTraceSpan          = internal.EventTraceSpan
	EventRunMetrics         = internal.EventRunMetrics
	EventRunComplete        = internal.EventRunComplete
	EventRunError           = internal.EventRunError
)

const (
	RunStatusCompleted       = internal.RunStatusCompleted
	RunStatusFailed          = internal.RunStatusFailed
	RunStatusInterrupted     = internal.RunStatusInterrupted
	RunStatusAwaitingApproval = internal.RunStatusAwaitingApproval
)

// BuiltinTools is the registry of built-in tools (calculator, http_fetch,
// now, file_read, file_write, file_list, web_search, shell_exec).
var BuiltinTools = internal.BuiltinTools

// ToolsByName indexes a tool slice by name.
func ToolsByName(tools []*Tool) map[string]*Tool {
	return internal.ToolsByName(tools)
}
