package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ToolContext gives a tool access to the run environment: the agent name,
// run id, and HITL primitives (RequestApproval).
type ToolContext struct {
	AgentName string
	RunID     string
	// Approval is set by the run loop when a human decision is available
	// for the pending approval.
	Approval map[string]bool
	// HTTP is a shared client for tools that call the network.
	HTTP *http.Client
	// Emit, when set, lets tools stream custom events into the run.
	Emit func(RunEvent)
}

// NewToolContext builds an empty tool context.
func NewToolContext(agentName, runID string) *ToolContext {
	return &ToolContext{
		AgentName: agentName,
		RunID:     runID,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// RequestApproval pauses the run and asks a human to approve or reject
// action. It returns nil when approved, an error when rejected, and
// interrupts the run until a decision is made.
func (tc *ToolContext) RequestApproval(action, summary string, ctxValue map[string]any) error {
	if tc.Approval != nil && len(tc.Approval) > 0 {
		// A decision has been injected for this approval.
		for _, approved := range tc.Approval {
			if approved {
				return nil
			}
			return NewError(KindInterrupt, fmt.Sprintf("action %q rejected by human", action))
		}
	}
	return &ApprovalRequiredError{Action: action, Summary: summary, Context: ctxValue}
}

// Tool is a capability an agent can invoke. Parameters is a JSON Schema
// document describing the argument shape.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Run executes the tool. It receives the parsed arguments and returns
	// a JSON-marshalable result. Return *ApprovalRequiredError (or call
	// ctx.RequestApproval) to pause for human-in-the-loop approval.
	Run func(ctx context.Context, tc *ToolContext, args json.RawMessage) (any, error)
	// cacheKey, when non-empty, deduplicates identical calls within a run
	// (used for replay-safe HITL resume).
	cacheKey string
}

// NewTool builds a typed tool from a Go function. The generic parameter T
// defines the argument shape; a JSON Schema is derived from it via
// reflection, and arguments are validated + decoded before fn runs.
func NewTool[T any](name, description string, fn func(ctx context.Context, tc *ToolContext, args T) (any, error)) (*Tool, error) {
	schema, err := SchemaFromStruct((*T)(nil))
	if err != nil {
		return nil, err
	}
	params, err := schema.SchemaJSON()
	if err != nil {
		return nil, err
	}
	var zero T
	_ = zero
	return &Tool{
		Name:        name,
		Description: description,
		Parameters:  params,
		Run: func(ctx context.Context, tc *ToolContext, raw json.RawMessage) (any, error) {
			var args T
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, NewError(KindValidation, fmt.Sprintf("%s: invalid arguments: %v", name, err), err)
			}
			return fn(ctx, tc, args)
		},
	}, nil
}

// MustTool is NewTool but panics on construction errors — convenient for
// package-level tool definitions.
func MustTool[T any](name, description string, fn func(ctx context.Context, tc *ToolContext, args T) (any, error)) *Tool {
	t, err := NewTool(name, description, fn)
	if err != nil {
		panic(err)
	}
	return t
}

// ToolsByName indexes a tool list.
func ToolsByName(tools []*Tool) map[string]*Tool {
	m := make(map[string]*Tool, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return m
}

// ---------------------------------------------------------------------------
// Built-in tools
// ---------------------------------------------------------------------------

// HTTPFetchArgs is the argument shape of the HTTPFetch tool.
type HTTPFetchArgs struct {
	URL     string            `json:"url" jsonschema:"The absolute URL to fetch"`
	Method  string            `json:"method,omitempty" jsonschema:"HTTP method (default GET), enum:GET,POST,PUT,DELETE,PATCH" enum:"GET,POST,PUT,DELETE,PATCH"`
	Headers map[string]string `json:"headers,omitempty" jsonschema:"Optional request headers"`
	Body    string            `json:"body,omitempty" jsonschema:"Request body for POST/PUT/PATCH"`
	MaxBytes int              `json:"maxBytes,omitempty" jsonschema:"Cap on response size (default 1MB)"`
}

// HTTPFetchResult is the result of the HTTPFetch tool.
type HTTPFetchResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// HTTPFetch lets an agent call any HTTP endpoint (a web search / API
// surrogate). Network is always scoped to the shared ToolContext client.
var HTTPFetch = MustTool[HTTPFetchArgs]("http_fetch", "Fetch a URL over HTTP(S) and return the response body", func(ctx context.Context, tc *ToolContext, args HTTPFetchArgs) (any, error) {
	method := strings.ToUpper(args.Method)
	if method == "" {
		method = "GET"
	}
	if args.URL == "" {
		return nil, NewError(KindValidation, "http_fetch: url is required")
	}
	maxBytes := args.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	client := tc.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, method, args.URL, strings.NewReader(args.Body))
	if err != nil {
		return nil, NewError(KindTool, "http_fetch: "+err.Error(), err)
	}
	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, NewError(KindTool, "http_fetch: "+err.Error(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
	if err != nil {
		return nil, NewError(KindTool, "http_fetch: read failed: "+err.Error(), err)
	}
	h := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			h[k] = vs[0]
		}
	}
	return HTTPFetchResult{Status: resp.StatusCode, Headers: h, Body: string(body)}, nil
})

// CalculatorArgs is the argument shape of the Calculator tool.
type CalculatorArgs struct {
	Expression string `json:"expression" jsonschema:"A simple arithmetic expression, e.g. (2 + 3) * 4"`
}

// Calculator evaluates simple arithmetic expressions safely (no eval).
var Calculator = MustTool[CalculatorArgs]("calculator", "Evaluate a simple arithmetic expression", func(ctx context.Context, tc *ToolContext, args CalculatorArgs) (any, error) {
	v, err := EvalExpression(args.Expression)
	if err != nil {
		return nil, NewError(KindTool, "calculator: "+err.Error(), err)
	}
	return map[string]any{"result": v}, nil
})

// NowArgs is the argument shape of the Now tool.
type NowArgs struct{}

// Now returns the current UTC time — handy for agents reasoning about time.
var Now = MustTool[NowArgs]("now", "Return the current UTC date and time", func(ctx context.Context, tc *ToolContext, args NowArgs) (any, error) {
	return map[string]any{"utc": time.Now().UTC().Format(time.RFC3339)}, nil
})

// BuiltinTools is the registry of built-in tools.
var BuiltinTools = []*Tool{HTTPFetch, Calculator, Now}
