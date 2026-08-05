// Package agent implements the core Agent: a provider-driven loop that
// streams events, executes tools (with caching and HITL approvals),
// persists memory, and retrieves knowledge.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"sync"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/knowledge"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/memory"
	"github.com/nemo715/Ernest/internal/storage"
)

// CostConfig prices tokens so the runner can estimate spend and enforce
// budget caps (per-1K-token costs in USD cents).
type CostConfig struct {
	InputPer1K  float64 `json:"inputPer1K"`
	OutputPer1K float64 `json:"outputPer1K"`
}

// Agent is a single autonomous worker. It is safe for concurrent use:
// each run operates on its own session.
type Agent struct {
	Name         string
	Description  string
	Instructions string
	Provider     llm.Provider
	Tools        []*core.Tool
	Memory       *memory.Memory
	Store        storage.SessionStore // convenience: auto-creates Memory when set
	Knowledge    *knowledge.KnowledgeBase
	MaxIterations int
	Temperature  *float64
	MaxTokens    int
	Stop         []string
	Hooks        Hooks

	// Guardrails (M3): budget, policy and redaction limits.
	MaxTotalTokens     int        // hard cap on summed input+output tokens per run (0 = off)
	MaxCostCents       float64    // hard cap on estimated spend per run (0 = off)
	Cost               *CostConfig // token prices for the cost estimate
	DenyTools          []string   // tool names the agent may never call
	RequireApprovalTools []string // tool names that always pause for HITL
	RedactPatterns     []string   // regexes; matches are replaced in user input
	RedactReplacement  string     // default "[REDACTED]"

	// approvalMu guards approvalSessions (approval id -> session id).
	approvalMu       sync.Mutex
	approvalSessions map[string]string
}

// registerApproval maps a HITL approval id to its session.
func (a *Agent) registerApproval(approvalID, sessionID string) {
	a.approvalMu.Lock()
	if a.approvalSessions == nil {
		a.approvalSessions = map[string]string{}
	}
	a.approvalSessions[approvalID] = sessionID
	a.approvalMu.Unlock()
}

// approvalSession looks up the session behind an approval id.
func (a *Agent) approvalSession(approvalID string) (string, bool) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if a.approvalSessions == nil {
		return "", false
	}
	s, ok := a.approvalSessions[approvalID]
	return s, ok
}

// Hooks are lifecycle callbacks fired during a run.
type Hooks struct {
	OnStart       func(ctx context.Context, input string)
	OnMessage     func(ctx context.Context, msg core.Message)
	OnToolCall    func(ctx context.Context, call core.ToolCall)
	OnToolResult  func(ctx context.Context, res core.ToolResult)
	OnFinish      func(ctx context.Context, result *core.RunResult)
}

// New builds an agent with defaults.
func New(name string, provider llm.Provider) *Agent {
	if provider == nil {
		provider = llm.NewMock(llm.MockConfig{})
	}
	return &Agent{Name: name, Provider: provider, MaxIterations: 8}
}

// RunOptions customise a single run.
type RunOptions struct {
	SessionID string
	UserID    string
	Temperature *float64
	MaxTokens   int
	Stop        []string
	ResponseSchema   *core.Schema
	ResponseSchemaName string
	MaxIterations int
	// SkipMemory disables session persistence (ephemeral run).
	SkipMemory bool
	// MaxTotalTokens caps the summed input+output tokens for this run
	// (overrides the agent's cap; 0 = agent default, -1 = no cap).
	MaxTotalTokens int
}

// Redact applies the agent's redaction patterns to s, replacing every
// match with the configured replacement (default "[REDACTED]").
func (a *Agent) Redact(s string) string {
	if len(a.RedactPatterns) == 0 {
		return s
	}
	repl := a.RedactReplacement
	if repl == "" {
		repl = "[REDACTED]"
	}
	for _, p := range a.RedactPatterns {
		if re, err := regexp.Compile(p); err == nil {
			s = re.ReplaceAllString(s, repl)
		}
	}
	return s
}

// Chat runs the agent synchronously and returns the full result.
func (a *Agent) Chat(ctx context.Context, input string, opts ...RunOptions) (*core.RunResult, error) {
	opt := RunOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.MaxIterations == 0 {
		opt.MaxIterations = a.MaxIterations
	}
	if opt.MaxIterations == 0 {
		opt.MaxIterations = 8
	}
	r, err := newRunner(a, opt)
	if err != nil {
		return nil, err
	}
	return r.run(ctx, input, false)
}

// Stream runs the agent and returns an event channel. The channel is
// closed after the final event (run.complete or run.error).
func (a *Agent) Stream(ctx context.Context, input string, opts ...RunOptions) (<-chan core.RunEvent, error) {
	opt := RunOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.MaxIterations == 0 {
		opt.MaxIterations = a.MaxIterations
	}
	if opt.MaxIterations == 0 {
		opt.MaxIterations = 8
	}
	r, err := newRunner(a, opt)
	if err != nil {
		return nil, err
	}
	ch := make(chan core.RunEvent, 256)
	r.emit = func(ev core.RunEvent) { ch <- ev }
	go func() {
		defer close(ch)
		_, _ = r.run(ctx, input, false)
	}()
	return ch, nil
}

// Resume continues an interrupted (HITL) run with a human decision.
// It is synchronous like Chat.
func (a *Agent) Resume(ctx context.Context, decision core.ApprovalDecision) (*core.RunResult, error) {
	r, err := newRunner(a, RunOptions{})
	if err != nil {
		return nil, err
	}
	r.approvals[decision.ApprovalID] = decision.Approved
	return r.resume(ctx, decision)
}

// StreamResume continues an interrupted (HITL) run and streams the
// events of the resumed execution (approval.resolved, tool.call,
// tool.result, ... run.complete). The channel is closed after the final
// event.
//
// Validation (approval known, session reachable) happens before the
// stream starts, so failures are returned as errors instead of surfacing
// as an empty channel.
func (a *Agent) StreamResume(ctx context.Context, decision core.ApprovalDecision) (<-chan core.RunEvent, error) {
	if _, ok := a.approvalSession(decision.ApprovalID); !ok {
		return nil, core.NewError(core.KindAgent, "unknown approval id "+decision.ApprovalID)
	}
	// A resume replays the stored session; ephemeral runs have no store
	// to replay from, so fail fast here rather than emitting nothing.
	if _, err := a.memory(&RunOptions{}); err != nil {
		return nil, err
	}
	r, err := newRunner(a, RunOptions{})
	if err != nil {
		return nil, err
	}
	r.approvals[decision.ApprovalID] = decision.Approved
	ch := make(chan core.RunEvent, 256)
	r.emit = func(ev core.RunEvent) { ch <- ev }
	go func() {
		defer close(ch)
		_, _ = r.resume(ctx, decision)
	}()
	return ch, nil
}

// HasApproval reports whether the agent knows the given pending
// approval (i.e. it belongs to one of this agent's sessions).
func (a *Agent) HasApproval(approvalID string) bool {
	_, ok := a.approvalSession(approvalID)
	return ok
}

// ClearMemory wipes the session history. Pass a session id to target a
// specific session; otherwise the agent's configured Memory session is
// used.
func (a *Agent) ClearMemory(ctx context.Context, sessionIDs ...string) error {
	sid := ""
	if len(sessionIDs) > 0 {
		sid = sessionIDs[0]
	}
	m, err := a.memory(nil)
	if err != nil {
		return err
	}
	if sid == "" {
		sid = m.SessionID
	}
	if sid == "" {
		return core.NewError(core.KindMemory, "no session id: pass one to ClearMemory or set memory.SessionID")
	}
	mem := memory.New(m.Store, memory.WithSessionID(sid), memory.WithUserID(m.UserID), memory.WithStrategy(m.Strategy))
	return mem.Clear(ctx)
}

// PendingApprovals lists unresolved HITL requests for the session.
func (a *Agent) PendingApprovals(ctx context.Context, sessionID string) ([]core.ApprovalRequest, error) {
	m, err := a.memory(&RunOptions{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	sess, err := m.Session(ctx)
	if err != nil {
		return nil, err
	}
	return sess.PendingApprovals, nil
}

// Memory returns the agent's memory (creating one from Store if needed).
func (a *Agent) memory(opt *RunOptions) (*memory.Memory, error) {
	if a.Memory != nil {
		return a.Memory, nil
	}
	if a.Store != nil {
		opts := []memory.Option{}
		if opt != nil {
			if opt.SessionID != "" {
				opts = append(opts, memory.WithSessionID(opt.SessionID))
			}
			if opt.UserID != "" {
				opts = append(opts, memory.WithUserID(opt.UserID))
			}
		}
		return memory.New(a.Store, opts...), nil
	}
	return nil, core.NewError(core.KindAgent, "agent "+a.Name+" has no Memory or Store configured")
}

// newID generates a random hex id with a prefix.
func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
