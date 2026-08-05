// MCP server side: exposes ernest agents as Model Context Protocol tools
// over stdio (newline-delimited JSON-RPC 2.0). Any MCP client — Claude
// Desktop, Cursor, another ernest instance — can then call your agents as
// ordinary tools via "ernest mcp-serve".
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/nemo715/Ernest/internal/agent"
)

// ServerOptions configures the MCP server.
type ServerOptions struct {
	Name    string // server name reported in initialize (default "ernest")
	Version string // server version (default "0.1.0")
}

// Server exposes a set of agents as MCP tools over stdio. Each agent
// becomes one tool taking {"input": "..."}; a tools/call runs the agent
// and returns its final output as text content.
type Server struct {
	name    string
	version string
	agents  []*agent.Agent

	mu     sync.Mutex
	nextID int64
}

// NewServer builds an MCP server from agents. Agents are safe for
// concurrent use, so concurrent tools/call requests are handled in
// parallel.
func NewServer(agents []*agent.Agent, opts ServerOptions) *Server {
	if opts.Name == "" {
		opts.Name = "ernest"
	}
	if opts.Version == "" {
		opts.Version = "0.1.0"
	}
	return &Server{name: opts.Name, version: opts.Version, agents: agents}
}

// AgentTool describes one agent-as-tool for tools/list.
type AgentTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolList returns the MCP tool descriptors for the server's agents.
func (s *Server) ToolList() []AgentTool {
	out := make([]AgentTool, 0, len(s.agents))
	for _, a := range s.agents {
		desc := "Run the ernest agent \"" + a.Name + "\"."
		if a.Description != "" {
			desc = a.Description + " (ernest agent \"" + a.Name + "\")"
		}
		out = append(out, AgentTool{
			Name:        a.Name,
			Description: desc,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "The message to send to the agent.",
					},
				},
				"required": []string{"input"},
			},
		})
	}
	return out
}

// agentToolNames is a convenience for error messages.
func (s *Server) agentToolNames() string {
	names := make([]string, 0, len(s.agents))
	for _, a := range s.agents {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// findAgent resolves a tool name to an agent.
func (s *Server) findAgent(name string) *agent.Agent {
	for _, a := range s.agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// ServeStdio speaks JSON-RPC 2.0 over in/out until EOF or ctx is done.
// Messages are newline-delimited JSON; a message without an "id" is a
// notification and gets no response. Any MCP client framing using the
// legacy Content-Length headers is also accepted.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	var (
		headerBuf strings.Builder
		skipBytes int
	)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimRight(sc.Text(), "\r")

		// A payload follows a Content-Length header block on its own line.
		if skipBytes > 0 {
			if len(line) < skipBytes {
				return fmt.Errorf("mcp server: short payload line (%d < %d bytes)", len(line), skipBytes)
			}
			payload := []byte(line[:skipBytes])
			skipBytes = 0
			response, err := s.handleMessage(ctx, payload)
			if err != nil {
				return err
			}
			if response == nil {
				continue // notification
			}
			if err := enc.Encode(response); err != nil {
				return err
			}
			continue
		}

		// Legacy header framing: collect Content-Length headers, then a
		// blank line announces the JSON payload follows on the next line.
		if strings.HasPrefix(line, "Content-Length:") || line == "" {
			headerBuf.WriteString(line)
			headerBuf.WriteByte('\n')
			if line == "" {
				hdr := headerBuf.String()
				headerBuf.Reset()
				if n, ok := parseContentLength(hdr); ok {
					skipBytes = n
				}
			}
			continue
		}

		response, err := s.handleMessage(ctx, []byte(line))
		if err != nil {
			return err
		}
		if response == nil {
			continue // notification
		}
		if err := enc.Encode(response); err != nil {
			return err
		}
	}
	return sc.Err()
}

func parseContentLength(hdr string) (int, bool) {
	for _, line := range strings.Split(hdr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(val), "%d", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// handleMessage dispatches one JSON-RPC message and returns the response
// to write (nil for notifications).
func (s *Server) handleMessage(ctx context.Context, payload []byte) (any, error) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return rpcServerError(nil, -32700, "parse error: "+err.Error()), nil
	}
	isNotification := len(msg.ID) == 0 || string(msg.ID) == "null"
	if msg.JSONRPC != "2.0" {
		return rpcServerError(nil, -32600, "invalid request: jsonrpc must be \"2.0\""), nil
	}
	if msg.Method == "" && !isNotification {
		return rpcServerError(nil, -32600, "invalid request: method required"), nil
	}

	switch msg.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = ProtocolVersion
		}
		result := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
		return rpcServerResult(msg.ID, result), nil
	case "notifications/initialized", "notifications/cancelled", "notifications/tools/list_changed":
		return nil, nil // notifications: no response
	case "ping":
		return rpcServerResult(msg.ID, map[string]any{}), nil
	case "tools/list":
		return rpcServerResult(msg.ID, map[string]any{"tools": s.ToolList()}), nil
	case "tools/call":
		return s.handleToolCall(ctx, msg.ID, msg.Params)
	case "resources/list", "resources/read", "prompts/list", "prompts/get":
		return rpcServerError(msg.ID, -32601, "method not supported: "+msg.Method), nil
	default:
		return rpcServerError(msg.ID, -32601, "method not found: "+msg.Method), nil
	}
}

func (s *Server) handleToolCall(ctx context.Context, id json.RawMessage, params json.RawMessage) (any, error) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return rpcServerError(id, -32602, "invalid params: "+err.Error()), nil
	}
	ag := s.findAgent(call.Name)
	if ag == nil {
		return rpcServerError(id, -32602, "unknown tool "+call.Name+" (agents: "+s.agentToolNames()+")"), nil
	}
	input, _ := call.Arguments["input"].(string)
	if input == "" {
		return rpcServerError(id, -32602, "tool "+call.Name+" requires a non-empty \"input\" string"), nil
	}

	res, err := ag.Chat(ctx, input, agent.RunOptions{SkipMemory: true})
	text := ""
	isError := false
	if err != nil {
		text = err.Error()
		isError = true
	} else {
		text = res.Output
		if res.Error != "" {
			text = res.Error
		}
		isError = res.Status != "completed"
	}
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
	return rpcServerResult(id, result), nil
}

// ---------------------------------------------------------------------------
// response helpers
// ---------------------------------------------------------------------------

func rpcServerResult(id json.RawMessage, result any) any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"result":  result,
	}
}

func rpcServerError(id json.RawMessage, code int, message string) any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
}

// rawID converts an id payload to an interface suitable for echoing. The
// MCP client in this package always sends integer ids; string ids from
// other clients are preserved.
func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	var n int64
	if err := json.Unmarshal(id, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(id, &s); err == nil {
		return s
	}
	return nil
}
