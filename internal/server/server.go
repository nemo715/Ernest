// Package server exposes ernest agents over HTTP with server-sent
// events: it is the playground backend consumed by the web UI, the CLI
// (`ernest playground`) and any HTTP client.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/audit"
	"github.com/nemo715/Ernest/internal/a2a"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/eval"
	"github.com/nemo715/Ernest/internal/storage"
)

// Options configures the server.
type Options struct {
	// Agents served by the API (required).
	Agents []*agent.Agent
	// Store backs /api/sessions. When nil, an in-memory store is used.
	Store storage.SessionStore
	// Static, when set, serves the built playground UI from this dir.
	Static string
	// Audit, when set, records tool calls, approvals and run outcomes
	// (exposed at GET /api/audit). A default auditor is used when nil.
	Audit *audit.Auditor
	// FailuresPath, when set, appends a FailureRecord (JSONL) for every
	// failed run — the production feed that `ernest eval --learn` turns
	// into regression scenarios. The record carries the user input, the
	// run error and every tool call/result observed during the run.
	FailuresPath string
}

// Server is the HTTP API. Create with New; mount Handler() anywhere.
type Server struct {
	agents map[string]*agent.Agent
	store  storage.SessionStore
	static string
	audit  *audit.Auditor
	a2a    *a2a.Server
	mux    *http.ServeMux

	tracesMu sync.Mutex
	traces   map[string]runTrace // runID -> spans + metrics

	// Failures feed: per-run event buffer flushed to the JSONL file on
	// run.complete (failed status). Only used when FailuresPath is set.
	failMu   sync.Mutex
	failPath string
	failBuf  map[string]*failRun // runID -> calls/results so far
}

// failRun is the event accumulation for one run, flushed when the run
// finishes (failed) to the failures feed.
type failRun struct {
	input   string
	calls   []core.ToolCall
	results []core.ToolResult
}

// IngestedTrace is one trace pushed from ANY framework over
// POST /api/traces (OTEL-style ingestion): the same spans/metrics the
// server records natively, so non-ernest agents land in the same
// store as ernest runs.
type IngestedTrace struct {
	TraceID    string           `json:"traceId"`
	Name       string           `json:"name,omitempty"`
	Agent      string           `json:"agent,omitempty"`
	Status     string           `json:"status,omitempty"`
	StartedAt  time.Time        `json:"startedAt,omitempty"`
	DurationMS int64            `json:"durationMs,omitempty"`
	Spans      []core.TraceSpan `json:"spans,omitempty"`
	Metrics    *core.RunMetrics `json:"metrics,omitempty"`
}

// runTrace is the stored trace of one run (for /api/runs/{id}/trace).
type runTrace struct {
	Spans   []core.TraceSpan `json:"spans"`
	Metrics *core.RunMetrics `json:"metrics,omitempty"`
	Source  string           `json:"source,omitempty"` // internal | ingested
}

// New builds a server. Agent names must be unique.
func New(opts Options) (*Server, error) {
	if len(opts.Agents) == 0 {
		return nil, core.NewError(core.KindValidation, "server: at least one agent is required")
	}
	raw := make([]*agent.Agent, len(opts.Agents))
	copy(raw, opts.Agents)
	s := &Server{
		agents: map[string]*agent.Agent{},
		store:  opts.Store,
		static: opts.Static,
		audit:  opts.Audit,
		traces: map[string]runTrace{},
		a2a:    a2a.NewServer(raw),
		mux:    http.NewServeMux(),
	}
	if opts.FailuresPath != "" {
		s.failPath = opts.FailuresPath
		s.failBuf = map[string]*failRun{}
	}
	if s.audit == nil {
		s.audit = audit.New()
	}
	if s.store == nil {
		s.store = storage.NewInMemoryStore()
	}
	for _, a := range opts.Agents {
		if a == nil || a.Name == "" {
			return nil, core.NewError(core.KindValidation, "server: agent without name")
		}
		if _, dup := s.agents[a.Name]; dup {
			return nil, core.NewError(core.KindValidation, "server: duplicate agent "+a.Name)
		}
		// Wrap: the served instance carries audit hooks; the caller's
		// agent is never mutated.
		wrapped := s.wrapAgent(a)
		// HITL resume replays the stored session: give agents without
		// their own Memory/Store the server's store so approvals pause
		// and resume out of the box.
		if wrapped.Store == nil && wrapped.Memory == nil {
			wrapped.Store = s.store
		}
		s.agents[a.Name] = wrapped
	}
	s.routes()
	return s, nil
}

// Agent returns the named agent (nil when absent).
func (s *Server) Agent(name string) *agent.Agent {
	return s.agents[name]
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Close releases the store.
func (s *Server) Close() error {
	return s.store.Close()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("POST /api/chat", s.handleChat)
	s.mux.HandleFunc("POST /api/approve", s.handleApprove)
	s.mux.HandleFunc("GET /api/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleSessionGet)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleSessionDelete)
	s.mux.HandleFunc("POST /api/traces", s.handleIngestTrace)
	s.mux.HandleFunc("GET /api/traces/{id}", s.handleRunTrace)
	s.mux.HandleFunc("GET /api/runs/{id}/trace", s.handleRunTrace) // legacy alias
	s.mux.HandleFunc("GET /api/audit", s.handleAudit)
	s.mux.HandleFunc("GET /ws/chat", s.handleWS)
	s.mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentWellKnown)
	s.mux.HandleFunc("POST /a2a/{agent}", s.handleA2A)
	s.mux.HandleFunc("GET /a2a/{agent}/card", s.handleA2ACard)
	if s.static != "" {
		s.mux.Handle("/", http.FileServer(http.Dir(s.static)))
	}
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type agentInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Model       string   `json:"model"`
	Provider    string   `json:"provider"`
	Tools       []string `json:"tools"`
}

type chatRequest struct {
	Agent         string   `json:"agent"`
	Input         string   `json:"input"`
	SessionID     string   `json:"sessionId,omitempty"`
	UserID        string   `json:"userId,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	MaxIterations int      `json:"maxIterations,omitempty"`
	SkipMemory    bool     `json:"skipMemory,omitempty"`
}

type approveRequest struct {
	Agent      string `json:"agent"`
	ApprovalID string `json:"approvalId"`
	Approved   bool   `json:"approved"`
	Note       string `json:"note,omitempty"`
}

type sessionInfo struct {
	ID               string    `json:"id"`
	AgentName        string    `json:"agentName"`
	UserID           string    `json:"userId,omitempty"`
	Messages         int       `json:"messages"`
	PendingApprovals int       `json:"pendingApprovals"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agents": len(s.agents)})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	out := make([]agentInfo, 0, len(s.agents))
	for _, a := range s.agents {
		info := agentInfo{
			Name:        a.Name,
			Description: a.Description,
			Provider:    providerID(a),
			Tools:       []string{},
		}
		if a.Provider != nil {
			info.Model = a.Provider.Model()
		}
		for _, t := range a.Tools {
			info.Tools = append(info.Tools, t.Name)
		}
		sort.Strings(info.Tools)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// handleChat runs an agent and streams the run events as SSE.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}
	ag := s.agents[req.Agent]
	if ag == nil {
		writeError(w, http.StatusNotFound, "unknown agent "+req.Agent)
		return
	}
	opts := agent.RunOptions{
		SessionID:     req.SessionID,
		UserID:        req.UserID,
		Temperature:   req.Temperature,
		MaxIterations: req.MaxIterations,
		SkipMemory:    req.SkipMemory,
	}
	ch, err := ag.Stream(r.Context(), req.Input, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.streamEvents(w, r, ch)
}

// handleApprove resolves a HITL approval and streams the resumed run.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var req approveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.ApprovalID == "" {
		writeError(w, http.StatusBadRequest, "approvalId is required")
		return
	}
	ag := s.agents[req.Agent]
	if ag == nil {
		writeError(w, http.StatusNotFound, "unknown agent "+req.Agent)
		return
	}
	if !ag.HasApproval(req.ApprovalID) {
		writeError(w, http.StatusNotFound, "unknown approval "+req.ApprovalID)
		return
	}
	s.audit.Record("approval.decided", req.Agent, "", map[string]any{
		"approvalId": req.ApprovalID,
		"approved":   req.Approved,
		"note":       req.Note,
	})
	ch, err := ag.StreamResume(r.Context(), core.ApprovalDecision{ApprovalID: req.ApprovalID, Approved: req.Approved, Note: req.Note})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.streamEvents(w, r, ch)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	agentName := r.URL.Query().Get("agent")
	sessions, err := s.store.List(r.Context(), agentName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]sessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionInfo{
			ID:               sess.ID,
			AgentName:        sess.AgentName,
			UserID:           sess.UserID,
			Messages:         len(sess.Messages),
			PendingApprovals: len(sess.PendingApprovals),
			UpdatedAt:        sess.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// streamEvents writes core.RunEvent frames as server-sent events. The
// stream closes when the channel closes or the client disconnects.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, ch <-chan core.RunEvent) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	cors(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.capture(ev)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", ev.Encode()); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// capture stores trace spans and metrics from any transport (SSE or WS)
// and feeds the failures sink (failed runs only).
func (s *Server) capture(ev core.RunEvent) {
	if ev.Span != nil {
		s.recordTrace(ev.RunID, *ev.Span)
	}
	if ev.Metrics != nil {
		s.recordMetrics(ev.RunID, *ev.Metrics)
	}
	s.captureFailure(ev)
}

// captureFailure accumulates tool calls/results per run and, when a
// run finishes failed, appends a FailureRecord to the failures feed.
// The feed is the production signal behind `ernest eval --learn`.
func (s *Server) captureFailure(ev core.RunEvent) {
	if s.failPath == "" {
		return
	}
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if ev.ToolCall != nil {
		b := s.failBuf[ev.RunID]
		if b == nil {
			b = &failRun{}
			s.failBuf[ev.RunID] = b
		}
		b.calls = append(b.calls, *ev.ToolCall)
		return
	}
	if ev.ToolResult != nil {
		b := s.failBuf[ev.RunID]
		if b == nil {
			b = &failRun{}
			s.failBuf[ev.RunID] = b
		}
		b.results = append(b.results, *ev.ToolResult)
		return
	}
	if ev.Result == nil {
		return
	}
	b := s.failBuf[ev.RunID]
	delete(s.failBuf, ev.RunID)
	if ev.Result.Status != core.RunStatusFailed {
		return
	}
	if b == nil {
		b = &failRun{}
	}
	if b.input == "" {
		for _, m := range ev.Result.Messages {
			if m.Role == core.RoleUser {
				b.input = m.Content
				break
			}
		}
	}
	if b.input == "" {
		return // nothing to learn from without the prompt
	}
	rec := eval.FailureRecord{
		RunID:       ev.Result.RunID,
		Agent:       ev.Agent,
		Input:       b.input,
		Output:      ev.Result.Output,
		Status:      string(ev.Result.Status),
		Error:       ev.Result.Error,
		ToolCalls:   b.calls,
		ToolResults: b.results,
		At:          time.Now(),
	}
	appendFailureRecord(s.failPath, rec)
}

// appendFailureRecord appends one record to a JSONL failures feed.
func appendFailureRecord(path string, rec eval.FailureRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// handleIngestTrace accepts a trace from any framework (OTEL-style):
// same shape as the server's native spans, stored under the trace id.
func (s *Server) handleIngestTrace(w http.ResponseWriter, r *http.Request) {
	var t IngestedTrace
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if t.TraceID == "" {
		writeError(w, http.StatusBadRequest, "traceId is required")
		return
	}
	if len(t.Spans) > 2000 {
		writeError(w, http.StatusBadRequest, "too many spans (max 2000)")
		return
	}
	s.tracesMu.Lock()
	s.traces[t.TraceID] = runTrace{Spans: t.Spans, Metrics: t.Metrics, Source: "ingested"}
	s.tracesMu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "traceId": t.TraceID, "spans": len(t.Spans)})
}


func (s *Server) recordTrace(runID string, sp core.TraceSpan) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	t := s.traces[runID]
	if t.Source == "" {
		t.Source = "internal"
	}
	t.Spans = append(t.Spans, sp)
	s.traces[runID] = t
}

func (s *Server) recordMetrics(runID string, m core.RunMetrics) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	t := s.traces[runID]
	t.Metrics = &m
	s.traces[runID] = t
}

// handleRunTrace returns the stored spans + metrics of one run.
func (s *Server) handleRunTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.tracesMu.Lock()
	t, ok := s.traces[id]
	s.tracesMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runId": id, "spans": t.Spans, "metrics": t.Metrics, "source": t.Source})
}

// handleAudit returns the newest audit entries (query: limit).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	entries := s.audit.List(limit)
	if entries == nil {
		entries = []audit.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// ---------------------------------------------------------------------------
// A2A (agent interoperability)
// ---------------------------------------------------------------------------

// handleAgentWellKnown serves the A2A discovery document at
// /.well-known/agent.json: the server plus cards for every agent.
func (s *Server) handleAgentWellKnown(w http.ResponseWriter, r *http.Request) {
	base := a2aBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "ernest",
		"version": "0.1.0",
		"url":     base + "/a2a",
		"agents":  s.a2a.AllCards(base),
	})
}

// handleA2ACard serves one agent's card.
func (s *Server) handleA2ACard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("agent")
	card, ok := s.a2a.AgentCard(name, a2aBaseURL(r))
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

// handleA2A dispatches A2A JSON-RPC requests.
func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("agent")
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	resp, err := s.a2a.HandleJSONRPC(r.Context(), name, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// a2aBaseURL derives the public base URL from the request.
func a2aBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	cors(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func providerID(a *agent.Agent) string {
	if a.Provider == nil {
		return ""
	}
	id := a.Provider.ID()
	if id == "" {
		id = strings.ToLower(a.Provider.Model())
	}
	return id
}

// wrapAgent builds a served copy of a with audit hooks. The caller's
// agent is never mutated; the copy must be a fresh struct (Agent holds a
// sync.Mutex, so value copies are illegal).
func (s *Server) wrapAgent(a *agent.Agent) *agent.Agent {
	n := &agent.Agent{
		Name:                 a.Name,
		Description:          a.Description,
		Instructions:         a.Instructions,
		Provider:             a.Provider,
		Tools:                a.Tools,
		Memory:               a.Memory,
		Store:                a.Store,
		Knowledge:            a.Knowledge,
		MaxIterations:        a.MaxIterations,
		Temperature:          a.Temperature,
		MaxTokens:            a.MaxTokens,
		Stop:                 a.Stop,
		MaxTotalTokens:       a.MaxTotalTokens,
		MaxCostCents:         a.MaxCostCents,
		Cost:                 a.Cost,
		DenyTools:            a.DenyTools,
		RequireApprovalTools: a.RequireApprovalTools,
		RedactPatterns:       a.RedactPatterns,
		RedactReplacement:    a.RedactReplacement,
	}
	h := a.Hooks
	name := a.Name
	ag := a
	h.OnToolCall = func(ctx context.Context, call core.ToolCall) {
		if ag.Hooks.OnToolCall != nil {
			ag.Hooks.OnToolCall(ctx, call)
		}
		s.audit.Record("tool.call", name, "", map[string]any{"tool": call.Name, "arguments": json.RawMessage(call.Arguments)})
	}
	h.OnFinish = func(ctx context.Context, res *core.RunResult) {
		if ag.Hooks.OnFinish != nil {
			ag.Hooks.OnFinish(ctx, res)
		}
		kind := "run.complete"
		if res.Status == core.RunStatusFailed {
			kind = "run.failed"
		}
		detail := map[string]any{"status": res.Status, "iterations": res.Metadata["iterations"], "durationMs": res.DurationMS, "costCents": 0}
		if res.Usage != nil {
			detail["tokens"] = res.Usage
		}
		s.audit.Record(kind, name, res.RunID, detail)
	}
	n.Hooks = h
	return n
}
