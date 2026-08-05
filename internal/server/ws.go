package server

// The /ws/chat endpoint is the realtime, bidirectional transport: the
// same RunEvent wire format as /api/chat SSE, plus client steering —
// interrupt a generation mid-stream, redirect it (steer), or resolve a
// HITL approval, all over one persistent connection.
//
// Client messages (JSON): {"type":"chat","agent","input","sessionId"},
// {"type":"steer","input"}, {"type":"interrupt"},
// {"type":"approve","approvalId","approved","note"}, {"type":"ping"}.
// Server frames: RunEvent JSON plus {"type":"ready"} on connect,
// {"type":"pong"}, {"type":"ack","action"} and
// {"type":"error","error"} for protocol problems.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/coder/websocket"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
)

type wsMessage struct {
	Type       string `json:"type"` // chat | steer | interrupt | approve | ping
	Agent      string `json:"agent,omitempty"`
	Input      string `json:"input,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	UserID     string `json:"userId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
	Approved   *bool  `json:"approved,omitempty"`
	Note       string `json:"note,omitempty"`
}

// wsConn is one connected client. At most one run is active per
// connection; steering queues a follow-up run for the same session.
type wsConn struct {
	s *Server
	c *websocket.Conn

	writeMu sync.Mutex // guards c.Write
	mu      sync.Mutex // guards run state
	running bool
	cancel  context.CancelFunc
	pendingSteer string // queued steer input (next run input)
	agent        string // agent of the most recent run (steer target)
	session      string // session of the most recent run
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "bye")
	conn := &wsConn{s: s, c: c}
	ctx := r.Context()

	names := make([]string, 0, len(s.agents))
	for n := range s.agents {
		names = append(names, n)
	}
	sort.Strings(names)
	conn.writeJSON(map[string]any{"type": "ready", "agents": names})

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return // client gone
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			conn.writeJSON(map[string]any{"type": "error", "error": "bad message: " + err.Error()})
			continue
		}
		switch msg.Type {
		case "ping":
			conn.writeJSON(map[string]any{"type": "pong"})
		case "chat":
			conn.startChat(ctx, msg)
		case "approve":
			conn.startApprove(ctx, msg)
		case "interrupt":
			conn.interrupt()
		case "steer":
			conn.steer(ctx, msg.Input)
		default:
			conn.writeJSON(map[string]any{"type": "error", "error": "unknown message type " + msg.Type})
		}
	}
}

func (w *wsConn) startChat(ctx context.Context, msg wsMessage) {
	ag := w.s.agents[msg.Agent]
	if ag == nil {
		w.writeJSON(map[string]any{"type": "error", "error": "unknown agent " + msg.Agent})
		return
	}
	if msg.Input == "" {
		w.writeJSON(map[string]any{"type": "error", "error": "input is required"})
		return
	}
	if !w.begin() {
		return
	}
	w.mu.Lock()
	w.agent = msg.Agent
	w.session = msg.SessionID
	w.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	w.setRun(cancel)
	go func() {
		defer w.finishRun()
		opts := agent.RunOptions{SessionID: msg.SessionID, UserID: msg.UserID}
		ch, err := ag.Stream(runCtx, msg.Input, opts)
		if err != nil {
			w.writeJSON(map[string]any{"type": "error", "error": err.Error()})
			return
		}
		for ev := range ch {
			w.s.capture(ev)
			w.write(ev.Encode())
		}
	}()
}

func (w *wsConn) startApprove(ctx context.Context, msg wsMessage) {
	ag := w.s.agents[msg.Agent]
	if ag == nil {
		w.writeJSON(map[string]any{"type": "error", "error": "unknown agent " + msg.Agent})
		return
	}
	if msg.ApprovalID == "" {
		w.writeJSON(map[string]any{"type": "error", "error": "approvalId is required"})
		return
	}
	if !ag.HasApproval(msg.ApprovalID) {
		w.writeJSON(map[string]any{"type": "error", "error": "unknown approval " + msg.ApprovalID})
		return
	}
	if !w.begin() {
		return
	}
	w.s.audit.Record("approval.decided", msg.Agent, "", map[string]any{"approvalId": msg.ApprovalID, "approved": msg.Approved, "note": msg.Note})

	runCtx, cancel := context.WithCancel(ctx)
	w.setRun(cancel)
	decision := core.ApprovalDecision{ApprovalID: msg.ApprovalID, Approved: msg.Approved != nil && *msg.Approved, Note: msg.Note}
	go func() {
		defer w.finishRun()
		ch, err := ag.StreamResume(runCtx, decision)
		if err != nil {
			w.writeJSON(map[string]any{"type": "error", "error": err.Error()})
			return
		}
		for ev := range ch {
			w.s.capture(ev)
			w.write(ev.Encode())
		}
	}()
}

// interrupt cancels the in-flight run; the provider stream unwinds and
// the loop emits run.error (interrupted) + run.complete (failed).
func (w *wsConn) interrupt() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
	w.writeJSON(map[string]any{"type": "ack", "action": "interrupt"})
}

// steer redirects the current run: the in-flight generation is cancelled
// and a new run with the steer text starts on the same session.
func (w *wsConn) steer(ctx context.Context, input string) {
	w.mu.Lock()
	w.pendingSteer = input
	agentName := w.agent
	session := w.session
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		w.writeJSON(map[string]any{"type": "ack", "action": "steer"})
		return
	}
	// No run in flight — start one directly.
	w.startChat(ctx, wsMessage{Type: "chat", Agent: agentName, Input: input, SessionID: session})
}

// begin reserves the connection for a new run; false when busy.
func (w *wsConn) begin() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		w.writeJSON(map[string]any{"type": "error", "error": "a run is already in progress (interrupt or wait)"})
		return false
	}
	w.running = true
	return true
}

func (w *wsConn) setRun(cancel context.CancelFunc) {
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
}

func (w *wsConn) finishRun() {
	w.mu.Lock()
	w.running = false
	w.cancel = nil
	steer := w.pendingSteer
	w.pendingSteer = ""
	agentName := w.agent
	session := w.session
	w.mu.Unlock()
	if steer != "" {
		w.startChat(context.Background(), wsMessage{Type: "chat", Agent: agentName, Input: steer, SessionID: session})
	}
}

// ---------------------------------------------------------------------------
// Frames
// ---------------------------------------------------------------------------

func (w *wsConn) write(data []byte) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_ = w.c.Write(context.Background(), websocket.MessageText, data)
}

func (w *wsConn) writeJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.write(data)
}
