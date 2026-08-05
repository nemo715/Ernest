// Package core is the public API for the ernest wire format: events,
// results, tools. It forwards to the implementation in ernest/internal/core.
package core

import internal "github.com/nemo715/Ernest/internal/core"

type (
	Tool       = internal.Tool
	ToolCall   = internal.ToolCall
	ToolResult = internal.ToolResult
	RunEvent   = internal.RunEvent
	RunResult  = internal.RunResult
	RunMetrics = internal.RunMetrics
	Usage      = internal.Usage
	Message    = internal.Message
)

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

// BuiltinTools is the registry of built-in tools (calculator,
// http_fetch, now).
var BuiltinTools = internal.BuiltinTools

// ToolsByName indexes a tool slice by name.
func ToolsByName(tools []*Tool) map[string]*Tool {
	return internal.ToolsByName(tools)
}
