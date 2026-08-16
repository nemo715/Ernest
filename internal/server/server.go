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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/a2a"
	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/audit"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/eval"
	"github.com/nemo715/Ernest/internal/storage"
	"github.com/nemo715/Ernest/internal/team"
	"github.com/nemo715/Ernest/internal/workflow"
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
	// Teams are declarative multi-agent teams built from the server's
	// (audit-wrapped) agents and served at /api/teams. The CLI passes
	// its config teams here.
	Teams []TeamSpec
	// Workflows are declarative step DAGs over the server's agents,
	// served at /api/workflows.
	Workflows []WorkflowSpec
}

// TeamSpec describes one team served at /api/teams.
type TeamSpec struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Leader        string   `json:"leader"`
	Members       []string `json:"members"`
	Process       string   `json:"process,omitempty"` // hierarchical (default) | sequential
	MaxIterations int      `json:"maxIterations,omitempty"`
	Instructions  string   `json:"instructions,omitempty"`
}

// GuardSpec is a declarative LLM-judged quality gate on a step output.
type GuardSpec struct {
	Rubric   string  `json:"rubric"`
	MinScore float64 `json:"minScore,omitempty"`
}

// WorkflowStepSpec describes one workflow step.
type WorkflowStepSpec struct {
	Name      string     `json:"name"`
	Agent     string     `json:"agent"`
	Prompt    string     `json:"prompt,omitempty"`
	DependsOn []string   `json:"dependsOn,omitempty"`
	Retries   int        `json:"retries,omitempty"`
	Guard     *GuardSpec `json:"guard,omitempty"`
}

// WorkflowSpec describes one step DAG served at /api/workflows.
type WorkflowSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Steps       []WorkflowStepSpec `json:"steps"`
	MaxRetries  int                `json:"maxRetries,omitempty"`
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

	// feedbackMu guards the in-memory feedback fallback (used when the
	// store does not implement storage.FeedbackStore).
	feedbackMu sync.Mutex
	feedback   map[string][]*storage.RunFeedback

	// Config-declared orchestrations, built from s.agents in New.
	teams     map[string]*team.Team
	workflows map[string]*workflow.Workflow

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
	Spans     []core.TraceSpan `json:"spans"`
	Metrics   *core.RunMetrics `json:"metrics,omitempty"`
	Source    string           `json:"source,omitempty"` // internal | ingested
	Agent     string           `json:"agent,omitempty"`
	StartedAt time.Time        `json:"startedAt,omitempty"`
	Context   *core.RunContext `json:"context,omitempty"` // what the model saw
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
	s.teams = map[string]*team.Team{}
	for _, spec := range opts.Teams {
		t, err := s.buildTeam(spec)
		if err != nil {
			return nil, err
		}
		s.teams[spec.Name] = t
	}
	s.workflows = map[string]*workflow.Workflow{}
	for _, spec := range opts.Workflows {
		wf, err := s.buildWorkflow(spec)
		if err != nil {
			return nil, err
		}
		s.workflows[spec.Name] = wf
	}
	s.routes()
	return s, nil
}

// buildTeam assembles a declarative team from the server's wrapped
// agents (so member runs keep audit hooks and HITL sessions).
func (s *Server) buildTeam(spec TeamSpec) (*team.Team, error) {
	if spec.Name == "" {
		return nil, core.NewError(core.KindValidation, "server: team without name")
	}
	leader := s.agents[spec.Leader]
	if leader == nil {
		return nil, core.NewError(core.KindValidation, fmt.Sprintf("team %q: unknown leader %q", spec.Name, spec.Leader))
	}
	members := make([]*agent.Agent, 0, len(spec.Members))
	for _, m := range spec.Members {
		if s.agents[m] == nil {
			return nil, core.NewError(core.KindValidation, fmt.Sprintf("team %q: unknown member %q", spec.Name, m))
		}
		members = append(members, s.agents[m])
	}
	t := team.New(spec.Name, leader, members...)
	t.Description = spec.Description
	t.Instructions = spec.Instructions
	if spec.MaxIterations > 0 {
		t.MaxIterations = spec.MaxIterations
	}
	if strings.EqualFold(spec.Process, "sequential") {
		t.Process = "sequential"
	} else {
		t.Process = "hierarchical"
	}
	return t, nil
}

// buildWorkflow assembles a declarative workflow from the server's
// wrapped agents.
func (s *Server) buildWorkflow(spec WorkflowSpec) (*workflow.Workflow, error) {
	specs := make([]workflow.StepSpec, 0, len(spec.Steps))
	for _, st := range spec.Steps {
		sp := workflow.StepSpec{
			Name:      st.Name,
			Agent:     st.Agent,
			Prompt:    st.Prompt,
			DependsOn: st.DependsOn,
			Retries:   st.Retries,
		}
		if st.Guard != nil {
			sp.Guard = &workflow.GuardSpec{Rubric: st.Guard.Rubric, MinScore: st.Guard.MinScore}
		}
		specs = append(specs, sp)
	}
	wf, err := workflow.Build(spec.Name, specs, s.agents, spec.MaxRetries)
	if err != nil {
		return nil, err
	}
	wf.Description = spec.Description
	return wf, nil
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
	s.mux.HandleFunc("GET /api/runs", s.handleRuns)
	s.mux.HandleFunc("POST /api/runs/{id}/feedback", s.handlePostFeedback)
	s.mux.HandleFunc("GET /api/runs/{id}/feedback", s.handleGetFeedback)
	s.mux.HandleFunc("GET /api/failures", s.handleFailures)
	s.mux.HandleFunc("GET /api/teams", s.handleTeams)
	s.mux.HandleFunc("POST /api/teams/{name}/run", s.handleTeamRun)
	s.mux.HandleFunc("GET /api/workflows", s.handleWorkflows)
	s.mux.HandleFunc("POST /api/workflows/{name}/run", s.handleWorkflowRun)
	s.mux.HandleFunc("GET /ws/chat", s.handleWS)
	s.mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentWellKnown)
	s.mux.HandleFunc("POST /a2a/{agent}", s.handleA2A)
	s.mux.HandleFunc("GET /a2a/{agent}/card", s.handleA2ACard)
	if s.static != "" {
		fs := http.FileServer(http.Dir(s.static))
		s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SPA fallback for the dev console, existence-aware:
			//   1. real exported pages (/runs/, /agents/, …) serve as-is;
			//   2. dynamic-route deep links (/runs/<id>) map to the
			//      static-export catchall page (<dir>/_/index.html) — the
			//      client reads the real id from the URL path;
			//   3. anything else falls back to the overview (index.html).
			p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			base := path.Base(p)
			first, _, _ := strings.Cut(p, "/")
			if p != "" && base != "." && !strings.Contains(base, ".") &&
				!strings.HasPrefix(first, "_") { // _next assets etc.
				if _, err := os.Stat(filepath.Join(s.static, filepath.FromSlash(p), "index.html")); err != nil {
					if d := path.Dir(p); d != "." {
						if _, err := os.Stat(filepath.Join(s.static, filepath.FromSlash(d), "_", "index.html")); err == nil {
							r.URL.Path = "/" + d + "/_/"
						} else {
							r.URL.Path = "/"
						}
					} else {
						r.URL.Path = "/"
					}
				}
			}
			fs.ServeHTTP(w, r)
		}))
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
// Teams + workflows
// ---------------------------------------------------------------------------

type teamInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Leader      string   `json:"leader"`
	Members     []string `json:"members"`
	Process     string   `json:"process"`
}

type workflowInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Steps       []string `json:"steps"`
}

type runRequest struct {
	Input string `json:"input"`
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	out := make([]teamInfo, 0, len(s.teams))
	for _, t := range s.teams {
		members := make([]string, 0, len(t.Members))
		for _, m := range t.Members {
			if m != nil {
				members = append(members, m.Name)
			}
		}
		out = append(out, teamInfo{Name: t.Name, Description: t.Description, Leader: t.Leader.Name, Members: members, Process: t.Process})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// handleTeamRun runs a team and streams the run events as SSE (same
// protocol as /api/chat).
func (s *Server) handleTeamRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}
	t := s.teams[r.PathValue("name")]
	if t == nil {
		writeError(w, http.StatusNotFound, "unknown team "+r.PathValue("name"))
		return
	}
	ch, err := t.Stream(r.Context(), req.Input, agent.RunOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.streamEvents(w, r, ch)
}

func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	out := make([]workflowInfo, 0, len(s.workflows))
	for _, wf := range s.workflows {
		steps := make([]string, 0, len(wf.Steps))
		for _, st := range wf.Steps {
			steps = append(steps, st.Name)
		}
		out = append(out, workflowInfo{Name: wf.Name, Description: wf.Description, Steps: steps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// handleWorkflowRun runs a workflow and streams the step events as SSE.
func (s *Server) handleWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}
	wf := s.workflows[r.PathValue("name")]
	if wf == nil {
		writeError(w, http.StatusNotFound, "unknown workflow "+r.PathValue("name"))
		return
	}
	ch, err := wf.Stream(r.Context(), req.Input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.streamEvents(w, r, ch)
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
	if ev.Agent != "" {
		s.recordAgent(ev.RunID, ev.Agent)
	}
	if ev.Result != nil && ev.Result.Context != nil {
		s.recordContext(ev.RunID, ev.Result.Context)
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
	if t.StartedAt.IsZero() {
		t.StartedAt = sp.StartedAt
	}
	t.Spans = append(t.Spans, sp)
	s.traces[runID] = t
}

func (s *Server) recordMetrics(runID string, m core.RunMetrics) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	t := s.traces[runID]
	t.Metrics = &m
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now().Add(-time.Duration(m.DurationMS) * time.Millisecond)
	}
	s.traces[runID] = t
}

func (s *Server) recordAgent(runID, agent string) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	t := s.traces[runID]
	t.Agent = agent
	s.traces[runID] = t
}

func (s *Server) recordContext(runID string, c *core.RunContext) {
	s.tracesMu.Lock()
	defer s.tracesMu.Unlock()
	t := s.traces[runID]
	t.Context = c
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
	writeJSON(w, http.StatusOK, map[string]any{"runId": id, "spans": t.Spans, "metrics": t.Metrics, "source": t.Source, "agent": t.Agent, "startedAt": t.StartedAt, "context": t.Context, "feedback": s.listFeedback(r.Context(), id)})
}

// handleRuns lists all traced runs, newest first (for the console).
type runSummary struct {
	RunID      string    `json:"runId"`
	Agent      string    `json:"agent,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	DurationMS int64     `json:"durationMs,omitempty"`
	Source     string    `json:"source,omitempty"`
	SpanCount  int       `json:"spanCount"`
	// FeedbackCount and Rating summarize human feedback (0 = none).
	FeedbackCount int `json:"feedbackCount,omitempty"`
	Rating        int `json:"rating,omitempty"` // latest rating 1..5
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	s.tracesMu.Lock()
	runs := make([]runSummary, 0, len(s.traces))
	for id, t := range s.traces {
		status := "running"
		var durationMS int64
		if t.Metrics != nil {
			status = t.Metrics.Status
			durationMS = t.Metrics.DurationMS
		}
		runs = append(runs, runSummary{
			RunID:      id,
			Agent:      t.Agent,
			Status:     status,
			StartedAt:  t.StartedAt,
			DurationMS: durationMS,
			Source:     t.Source,
			SpanCount:  len(t.Spans),
		})
	}
	s.tracesMu.Unlock()
	// Annotate with human feedback (cheap: local stores, small N).
	for i := range runs {
		fb := s.listFeedback(r.Context(), runs[i].RunID)
		runs[i].FeedbackCount = len(fb)
		if len(fb) > 0 {
			runs[i].Rating = fb[len(fb)-1].Rating
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	if runs == nil {
		runs = []runSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handlePostFeedback stores a human rating/comment on a run.
func (s *Server) handlePostFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.tracesMu.Lock()
	_, ok := s.traces[id]
	s.tracesMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run "+id)
		return
	}
	var body struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid feedback body: "+err.Error())
		return
	}
	if body.Rating < 1 || body.Rating > 5 {
		writeError(w, http.StatusBadRequest, "rating must be 1..5")
		return
	}
	f := &storage.RunFeedback{
		RunID:   id,
		Rating:  body.Rating,
		Comment: strings.TrimSpace(body.Comment),
	}
	if err := s.saveFeedback(r.Context(), f); err != nil {
		writeError(w, http.StatusInternalServerError, "save feedback: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": s.listFeedback(r.Context(), id)})
}

// handleGetFeedback lists the feedback for a run.
func (s *Server) handleGetFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.tracesMu.Lock()
	_, ok := s.traces[id]
	s.tracesMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": s.listFeedback(r.Context(), id)})
}

// saveFeedback persists feedback to the store when it supports it,
// falling back to the server's in-memory map.
func (s *Server) saveFeedback(ctx context.Context, f *storage.RunFeedback) error {
	if fs, ok := s.store.(storage.FeedbackStore); ok {
		return fs.SaveFeedback(ctx, f)
	}
	s.feedbackMu.Lock()
	defer s.feedbackMu.Unlock()
	s.feedback[f.RunID] = append(s.feedback[f.RunID], f)
	return nil
}

// listFeedback returns the feedback for a run (nil when none).
func (s *Server) listFeedback(ctx context.Context, runID string) []*storage.RunFeedback {
	if fs, ok := s.store.(storage.FeedbackStore); ok {
		out, err := fs.ListFeedback(ctx, runID)
		if err == nil {
			if out == nil {
				return []*storage.RunFeedback{}
			}
			return out
		}
	}
	s.feedbackMu.Lock()
	defer s.feedbackMu.Unlock()
	if s.feedback[runID] == nil {
		return []*storage.RunFeedback{}
	}
	return s.feedback[runID]
}

// handleFailures returns the tail of the failures feed (the production
// signal behind `ernest eval --learn`), newest first.
func (s *Server) handleFailures(w http.ResponseWriter, r *http.Request) {
	if s.failPath == "" {
		writeError(w, http.StatusNotFound, "no failures feed configured (set \"failures\" in ernest.json)")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	if limit > 200 {
		limit = 200
	}
	s.failMu.Lock()
	data, err := os.ReadFile(s.failPath)
	s.failMu.Unlock()
	if err != nil {
		writeError(w, http.StatusNotFound, "failures feed not readable: "+err.Error())
		return
	}
	var records []json.RawMessage
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		records = append(records, json.RawMessage(line))
	}
	// Newest first, capped.
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	if records == nil {
		records = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
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
