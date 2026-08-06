// Package mcp implements a Model Context Protocol client. It connects to
// MCP servers over streamable HTTP or stdio, discovers their tools, and
// exposes them as regular ernest tools for agents.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

// ProtocolVersion is the MCP protocol version this client speaks.
const ProtocolVersion = "2025-06-18"

// Options configures an MCP client.
type Options struct {
	Name    string        // client name sent in initialize (default "ernest")
	Version string        // client version (default "0.1.0")
	Timeout time.Duration // per-request timeout (default 60s)
	Env     []string      // extra environment variables for stdio servers
}

// Tool is one MCP tool descriptor.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ContentBlock is one item of MCP tool result content.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Result is a tools/call result.
type Result struct {
	Content    []ContentBlock  `json:"content,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	Structured json.RawMessage `json:"structuredContent,omitempty"`
}

// Text concatenates the text content blocks of the result.
func (r Result) Text() string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// Client is an MCP client bound to a single server. It is safe for
// concurrent use over HTTP; stdio requests are serialised by the
// transport's single reader.
type Client struct {
	name    string
	version string
	timeout time.Duration
	tr      transport

	mu       sync.Mutex
	nextID   int
	tools    []Tool // cached tool list
	initOnce sync.Once
	initErr  error
}

// transport abstracts the wire (streamable HTTP or stdio).
type transport interface {
	// request performs a JSON-RPC round trip and returns the result payload.
	request(ctx context.Context, id int, method string, params any) (json.RawMessage, error)
	// notify sends a JSON-RPC notification (no response expected).
	notify(ctx context.Context, method string, params any) error
	close() error
}

// NewHTTP connects to a streamable-HTTP MCP server endpoint. The
// handshake happens lazily on the first call.
func NewHTTP(endpoint string, opts Options) (*Client, error) {
	if endpoint == "" {
		return nil, core.NewError(core.KindMCP, "mcp: http endpoint is required")
	}
	tr, err := newHTTPTransport(endpoint)
	if err != nil {
		return nil, err
	}
	c := newClient(opts)
	c.tr = tr
	return c, nil
}

// NewStdio launches an MCP server process and speaks newline-delimited
// JSON-RPC over stdin/stdout.
func NewStdio(command string, args []string, opts Options) (*Client, error) {
	if command == "" {
		return nil, core.NewError(core.KindMCP, "mcp: stdio command is required")
	}
	tr, err := newStdioTransport(command, args, opts.Env)
	if err != nil {
		return nil, err
	}
	c := newClient(opts)
	c.tr = tr
	return c, nil
}

func newClient(opts Options) *Client {
	if opts.Name == "" {
		opts.Name = "ernest"
	}
	if opts.Version == "" {
		opts.Version = "0.1.0"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	return &Client{name: opts.Name, version: opts.Version, timeout: opts.Timeout}
}

// Initialize performs the MCP handshake (initialize + initialized
// notification). It is idempotent and runs lazily when not called.
func (c *Client) Initialize(ctx context.Context) error {
	return c.initialize(ctx)
}

func (c *Client) initialize(ctx context.Context) error {
	c.initOnce.Do(func() {
		c.initErr = c.doInitialize(ctx)
	})
	return c.initErr
}

func (c *Client) doInitialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": c.name, "version": c.version},
	}
	if _, err := c.request(ctx, "initialize", params); err != nil {
		return err
	}
	// The server's negotiated version is intentionally not enforced:
	// acknowledge with the initialized notification and move on.
	return c.tr.notify(ctx, "notifications/initialized", map[string]any{})
}

// ListTools returns the server's tool list (cached afterwards).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	if c.tools != nil {
		tools := c.tools
		c.mu.Unlock()
		return tools, nil
	}
	c.mu.Unlock()
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad tools/list result: "+err.Error(), err)
	}
	c.mu.Lock()
	c.tools = out.Tools
	c.mu.Unlock()
	return out.Tools, nil
}

// Call invokes an MCP tool. arguments may be nil for argument-free tools.
func (c *Client) Call(ctx context.Context, name string, arguments map[string]any) (*Result, error) {
	if name == "" {
		return nil, core.NewError(core.KindValidation, "mcp: tool name is required")
	}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	params := map[string]any{"name": name}
	if len(arguments) > 0 {
		params["arguments"] = arguments
	}
	res, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var out Result
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad tools/call result: "+err.Error(), err)
	}
	return &out, nil
}

// Resource is one entry of a server's resources/list (MCP resources:
// context the host can surface to the model — documents, logs, files).
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceContent is one item of a resources/read result.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64-encoded binary
}

// PromptArgument describes one argument of an MCP prompt template.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt is one entry of a server's prompts/list (reusable prompt
// templates that the host can expand and feed to a model).
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptContent is one message's content in a prompts/get result.
type PromptContent struct {
	Type string `json:"type"` // text | image
	Text string `json:"text,omitempty"`
}

// PromptMessage is one message of a prompts/get result.
type PromptMessage struct {
	Role    string        `json:"role"` // user | assistant
	Content PromptContent `json:"content"`
}

// PromptResult is the expanded prompt returned by prompts/get.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// Resources lists the server's MCP resources.
func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "resources/list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad resources/list result: "+err.Error(), err)
	}
	return out.Resources, nil
}

// ReadResource reads one MCP resource by URI.
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	if uri == "" {
		return nil, core.NewError(core.KindValidation, "mcp: resource uri is required")
	}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	var out struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad resources/read result: "+err.Error(), err)
	}
	return out.Contents, nil
}

// Prompts lists the server's prompt templates.
func (c *Client) Prompts(ctx context.Context) ([]Prompt, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "prompts/list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Prompts []Prompt `json:"prompts"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad prompts/list result: "+err.Error(), err)
	}
	return out.Prompts, nil
}

// GetPrompt expands one prompt template with the given arguments.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) (*PromptResult, error) {
	if name == "" {
		return nil, core.NewError(core.KindValidation, "mcp: prompt name is required")
	}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = args
	}
	res, err := c.request(ctx, "prompts/get", params)
	if err != nil {
		return nil, err
	}
	var out PromptResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: bad prompts/get result: "+err.Error(), err)
	}
	return &out, nil
}

// AsCoreTools converts the server's tools into ernest tools, ready to be
// attached to an agent. Tool calls are forwarded to the MCP server; a
// result flagged isError surfaces as a tool error to the model.
func (c *Client) AsCoreTools(ctx context.Context) ([]*core.Tool, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*core.Tool, 0, len(tools))
	for _, t := range tools {
		tool := t // capture
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, &core.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  schema,
			Run: func(ctx context.Context, tc *core.ToolContext, args json.RawMessage) (any, error) {
				var arguments map[string]any
				if len(args) > 0 {
					if err := json.Unmarshal(args, &arguments); err != nil {
						return nil, core.NewError(core.KindValidation, "mcp "+tool.Name+": "+err.Error(), err)
					}
				}
				res, err := c.Call(ctx, tool.Name, arguments)
				if err != nil {
					return nil, err
				}
				if res.IsError {
					return nil, core.NewError(core.KindTool, "mcp tool "+tool.Name+" failed: "+res.Text())
				}
				return res, nil
			},
		})
	}
	return out, nil
}

// Close releases the connection: it terminates the HTTP session (DELETE)
// or the stdio server process.
func (c *Client) Close() error {
	if c.tr == nil {
		return nil
	}
	return c.tr.close()
}

// request performs a JSON-RPC round trip with the configured timeout.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	return c.tr.request(ctx, id, method, params)
}

// ---------------------------------------------------------------------------
// JSON-RPC wire helpers
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func marshalRPC(id int, method string, params any) []byte {
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		req.Params = params
	}
	b, _ := json.Marshal(req)
	return b
}

func marshalNotification(method string, params any) []byte {
	req := rpcNotification{JSONRPC: "2.0", Method: method}
	if params != nil {
		req.Params = params
	}
	b, _ := json.Marshal(req)
	return b
}

// rpcResultError converts a JSON-RPC error into a typed mcp error.
func rpcResultError(e *rpcError) error {
	return core.NewError(core.KindMCP, fmt.Sprintf("mcp: %s (code %d)", e.Message, e.Code))
}
