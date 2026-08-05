// Package a2a implements the Agent2Agent protocol: ernest agents are
// exposed as interoperable JSON-RPC 2.0 endpoints (message/send,
// tasks/send|get|cancel, agent cards) that any A2A client can call, and a
// companion "a2a_call" tool lets ernest agents call remote agents.
package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"ernest/internal/agent"
)

// ProtocolVersion is the A2A protocol version spoken by this server.
const ProtocolVersion = "0.2"

// MessageState mirrors the A2A message/task lifecycle.
type MessageState string

const (
	StateWorking        MessageState = "working"
	StateCompleted      MessageState = "completed"
	StateFailed         MessageState = "failed"
	StateInputRequired  MessageState = "input-required"
	StateSubmitted      MessageState = "submitted"
	StateCanceled       MessageState = "canceled"
)

// Part is one content block of an A2A message.
type Part struct {
	Kind string `json:"kind"` // text | file | data
	Text string `json:"text,omitempty"`
}

// Message is an A2A message.
type Message struct {
	Role      string `json:"role"` // user | agent
	MessageID string `json:"messageId"`
	Parts     []Part `json:"parts"`
}

// AgentCard describes one agent for discovery.
type AgentCard struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	URL          string   `json:"url"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"` // streaming | pushNotifications
	Skills       []string `json:"skills,omitempty"`
}

// Task is the long-running unit of work of the A2A protocol.
type Task struct {
	ID         string       `json:"id"`
	State      MessageState `json:"state"`
	Messages   []Message    `json:"messages,omitempty"`
	Artifacts  []Part       `json:"artifacts,omitempty"`
	Error      *TaskError   `json:"error,omitempty"`
	CreatedAt  time.Time    `json:"createdAt"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty"`
}

// TaskError carries task failure details.
type TaskError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// server
// ---------------------------------------------------------------------------

// Server is the A2A JSON-RPC endpoint for a set of agents.
type Server struct {
	agents []*agent.Agent

	mu      sync.Mutex
	tasks   map[string]*Task
	cancels map[string]context.CancelFunc
	next    int
}

// NewServer builds an A2A server over agents.
func NewServer(agents []*agent.Agent) *Server {
	return &Server{agents: agents, tasks: map[string]*Task{}, cancels: map[string]context.CancelFunc{}}
}

// findAgent resolves an agent name.
func (s *Server) findAgent(name string) *agent.Agent {
	for _, a := range s.agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func agentNames(agents []*agent.Agent) string {
	out := ""
	for i, a := range agents {
		if i > 0 {
			out += ", "
		}
		out += a.Name
	}
	return out
}

// AgentCard returns the card for one agent, with url resolved against
// the public base URL of this server.
func (s *Server) AgentCard(name, baseURL string) (AgentCard, bool) {
	a := s.findAgent(name)
	if a == nil {
		return AgentCard{}, false
	}
	skills := []string{}
	if len(a.Tools) > 0 {
		for _, t := range a.Tools {
			skills = append(skills, t.Name)
		}
	}
	return AgentCard{
		Name:         a.Name,
		Description:  a.Description,
		URL:          baseURL + "/a2a/" + a.Name,
		Version:      "0.1.0",
		Capabilities: []string{"streaming"},
		Skills:       skills,
	}, true
}

// AllCards returns cards for every agent.
func (s *Server) AllCards(baseURL string) []AgentCard {
	out := make([]AgentCard, 0, len(s.agents))
	for _, a := range s.agents {
		if c, ok := s.AgentCard(a.Name, baseURL); ok {
			out = append(out, c)
		}
	}
	return out
}

// HandleJSONRPC dispatches one JSON-RPC request body for the agent named
// in the URL path and returns the response object.
func (s *Server) HandleJSONRPC(ctx context.Context, agentName string, body []byte) (any, error) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return rpcErr(nil, -32700, "parse error"), nil
	}
	if req.JSONRPC != "2.0" {
		return rpcErr(req.ID, -32600, "jsonrpc must be \"2.0\""), nil
	}
	if req.Method == "" {
		return rpcErr(req.ID, -32600, "method required"), nil
	}

	switch req.Method {
	case "initialize":
		return rpcOK(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"streaming": true, "pushNotifications": false},
			"agentCapabilities": map[string]any{
				"name":         agentName,
				"streaming":    true,
				"pushNotifications": false,
			},
		}), nil
	case "agent/card":
		base := "http://localhost"
		card, ok := s.AgentCard(agentName, base)
		if !ok {
			return rpcErr(req.ID, -32002, "agent not found: "+agentName), nil
		}
		return rpcOK(req.ID, card), nil
	case "message/send":
		return s.messageSend(ctx, agentName, req.ID, req.Params)
	case "tasks/send":
		return s.taskSend(ctx, agentName, req.ID, req.Params)
	case "tasks/get":
		return s.taskGet(req.ID, req.Params)
	case "tasks/cancel":
		return s.taskCancel(req.ID, req.Params)
	case "message/stream":
		return rpcErr(req.ID, -32601, "message/stream uses SSE; call GET /a2a/"+agentName+"/stream"), nil
	default:
		return rpcErr(req.ID, -32601, "method not found: "+req.Method), nil
	}
}

// messageSend runs the agent synchronously and returns the reply message.
func (s *Server) messageSend(ctx context.Context, agentName string, id, params json.RawMessage) (any, error) {
	var p struct {
		Message Message `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return rpcErr(id, -32602, "invalid params: "+err.Error()), nil
	}
	text := textOf(p.Message)
	if text == "" {
		return rpcErr(id, -32602, "message requires a text part"), nil
	}
	ag := s.findAgent(agentName)
	if ag == nil {
		return rpcErr(id, -32002, "agent not found: "+agentName+" (have: "+agentNames(s.agents)+")"), nil
	}
	res, err := ag.Chat(ctx, text, agent.RunOptions{SkipMemory: true})
	state := StateCompleted
	reply := res.Output
	if err != nil {
		state = StateFailed
		reply = err.Error()
	} else if res.Error != "" {
		state = StateFailed
		reply = res.Error
	}
	return rpcOK(id, map[string]any{
		"id":    "msg_" + newID(),
		"state": state,
		"message": Message{
			Role:      "agent",
			MessageID: "msg_" + newID(),
			Parts:     []Part{{Kind: "text", Text: reply}},
		},
	}), nil
}

// taskSend starts an asynchronous run and returns the working task.
func (s *Server) taskSend(ctx context.Context, agentName string, id, params json.RawMessage) (any, error) {
	var p struct {
		Message Message `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return rpcErr(id, -32602, "invalid params: "+err.Error()), nil
	}
	text := textOf(p.Message)
	if text == "" {
		return rpcErr(id, -32602, "message requires a text part"), nil
	}
	ag := s.findAgent(agentName)
	if ag == nil {
		return rpcErr(id, -32002, "agent not found: "+agentName+" (have: "+agentNames(s.agents)+")"), nil
	}

	t := &Task{ID: "task_" + newID(), State: StateSubmitted, CreatedAt: time.Now()}
	tctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.cancels[t.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.cancels, t.ID)
			s.mu.Unlock()
		}()
		res, err := ag.Chat(tctx, text, agent.RunOptions{SkipMemory: true})
		fin := time.Now()
		s.mu.Lock()
		defer s.mu.Unlock()
		t.FinishedAt = &fin
		if tctx.Err() != nil || t.State == StateCanceled {
			t.State = StateCanceled
			return
		}
		reply := ""
		switch {
		case err != nil:
			t.State = StateFailed
			reply = err.Error()
		case res.Error != "":
			t.State = StateFailed
			reply = res.Error
		default:
			t.State = StateCompleted
			reply = res.Output
		}
		t.Messages = []Message{{
			Role:      "agent",
			MessageID: "msg_" + newID(),
			Parts:     []Part{{Kind: "text", Text: reply}},
		}}
		t.Artifacts = []Part{{Kind: "text", Text: reply}}
	}()
	return rpcOK(id, t), nil
}

func (s *Server) taskGet(id, params json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return rpcErr(id, -32602, "invalid params: "+err.Error()), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[p.ID]
	if !ok {
		return rpcErr(id, -32002, "task not found: "+p.ID), nil
	}
	return rpcOK(id, t), nil
}

func (s *Server) taskCancel(id, params json.RawMessage) (any, error) {
	// Cancellation is cooperative: the run ctx is canceled by dropping
	// the task state. In this implementation tasks cancel via their own
	// context; expose the protocol shape and mark canceled.
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return rpcErr(id, -32602, "invalid params: "+err.Error()), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[p.ID]
	if !ok {
		return rpcErr(id, -32002, "task not found: "+p.ID), nil
	}
	if t.State == StateCompleted || t.State == StateFailed || t.State == StateCanceled {
		return rpcOK(id, t), nil
	}
	t.State = StateCanceled
	if cancel, ok := s.cancels[p.ID]; ok {
		cancel()
	}
	return rpcOK(id, t), nil
}

func textOf(m Message) string {
	for _, p := range m.Parts {
		if p.Kind == "text" && p.Text != "" {
			return p.Text
		}
	}
	return ""
}

func rpcOK(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawID(id), "result": result}
}

func rpcErr(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"error":   map[string]any{"code": code, "message": message},
	}
}

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

var idMu sync.Mutex
var idSeq int64

func newID() string {
	idMu.Lock()
	defer idMu.Unlock()
	idSeq++
	return fmt.Sprintf("%d", time.Now().UnixNano()) + "_" + fmt.Sprintf("%d", idSeq)
}
