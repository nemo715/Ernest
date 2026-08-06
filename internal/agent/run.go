package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/memory"
	"github.com/nemo715/Ernest/internal/storage"
)

// errAwaitingApproval pauses the run until a human decision arrives.
var errAwaitingApproval = errors.New("awaiting human approval")

// runner carries the state of one run.
type runner struct {
	agent   *Agent
	opts    RunOptions
	runID   string
	started time.Time

	mem      *memory.Memory
	session  *storage.Session
	ephemeral *storage.Session // used when memory is disabled

	emit    func(core.RunEvent)
	approvals map[string]bool // injected HITL decisions
	usage   *core.Usage
	iterations int
	callCounts map[string]int // identical tool-call signature -> count (runaway detection)
	spans   []core.TraceSpan // closed spans, in order (trace.span events)
	lastContext *core.RunContext // context of the most recent provider request
}

func newRunner(a *Agent, opt RunOptions) (*runner, error) {
	r := &runner{
		agent:     a,
		opts:      opt,
		runID:     newID("run"),
		started:   time.Now(),
		approvals: map[string]bool{},
		callCounts: map[string]int{},
	}
	if !opt.SkipMemory && (a.Memory != nil || a.Store != nil) {
		var err error
		r.mem, err = a.memory(&opt)
		if err != nil {
			return nil, err
		}
	}
	// Without Memory/Store the run is ephemeral: zero-config agents work
	// out of the box (persistence is opt-in).
	return r, nil
}

func (r *runner) emitEvent(ev core.RunEvent) {
	if r.emit != nil {
		r.emit(ev)
	}
}

// run executes the full loop. When resume is true the user message has
// already been recorded and the loop continues from tool execution.
func (r *runner) run(ctx context.Context, input string, resume bool) (result *core.RunResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			// Named returns: a recovered panic must still surface as a
			// failed result, otherwise the caller sees (nil, nil).
			result, err = r.fail(ctx, fmt.Sprintf("panic in run: %v", p))
		}
	}()
	// Redact PII in the user input before it enters history or the model.
	input = r.agent.Redact(input)
	r.emitEvent(core.RunEvent{Type: core.EventRunStart, RunID: r.runID, Agent: r.agent.Name, Data: json.RawMessage(fmt.Sprintf(`{"input":%q}`, input))})
	if h := r.agent.Hooks.OnStart; h != nil {
		h(ctx, input)
	}

	sess, err := r.loadSession(ctx)
	if err != nil {
		return r.fail(ctx, err.Error())
	}
	r.session = sess

	if !resume {
		userMsg := core.NewUserMessage(input)
		sess.Messages = append(sess.Messages, userMsg)
		if err := r.save(ctx); err != nil {
			return r.fail(ctx, err.Error())
		}
	}

	result, err = r.loop(ctx)
	if err != nil {
		if errors.Is(err, errAwaitingApproval) {
			out := r.buildResult(core.RunStatusAwaitingApproval, "", nil)
			return out, nil
		}
		if errors.Is(err, context.Canceled) {
			return r.fail(ctx, "interrupted by user")
		}
		return r.fail(ctx, err.Error())
	}
	return result, nil
}

// resume continues a paused run with a human decision.
func (r *runner) resume(ctx context.Context, decision core.ApprovalDecision) (*core.RunResult, error) {
	sessionID, ok := r.agent.approvalSession(decision.ApprovalID)
	if !ok {
		return nil, core.NewError(core.KindAgent, "unknown approval id "+decision.ApprovalID)
	}
	// The runner was built without session id; bind to the stored one.
	if r.opts.SessionID == "" {
		r.opts.SessionID = sessionID
	}
	var err error
	r.mem, err = r.agent.memory(&r.opts)
	if err != nil {
		return nil, err
	}
	sess, err := r.mem.Session(ctx)
	if err != nil {
		return nil, err
	}
	r.session = sess
	r.runID = r.session.Metadata["lastRunID"]

	// Resolve the approval record.
	now := time.Now().UTC()
	var pending []core.ApprovalRequest
	// Loaded sessions may have a nil ResolvedApprovals map
	// (resolvedApprovals is omitempty in storage).
	if sess.ResolvedApprovals == nil {
		sess.ResolvedApprovals = map[string]core.ApprovalDecision{}
	}
	for _, ap := range sess.PendingApprovals {
		if ap.ID == decision.ApprovalID {
			ap.Status = "rejected"
			if decision.Approved {
				ap.Status = "approved"
			}
			ap.Note = decision.Note
			ap.ResolvedAt = &now
			sess.ResolvedApprovals[ap.ID] = decision
			r.emitEvent(core.RunEvent{Type: core.EventApprovalResolved, RunID: r.runID, Agent: r.agent.Name, Approval: &ap})
			continue
		}
		pending = append(pending, ap)
	}
	sess.PendingApprovals = pending

	// Execute the blocked tool calls (all of them for this approval).
	var blocked []storage.PendingToolCall
	var remaining []storage.PendingToolCall
	for _, pc := range sess.PendingCalls {
		if pc.ApprovalID == decision.ApprovalID {
			blocked = append(blocked, pc)
		} else {
			remaining = append(remaining, pc)
		}
	}
	sess.PendingCalls = remaining

	if len(blocked) > 0 {
		if _, _, execErr := r.executeTurn(ctx, blocked); execErr != nil {
			return r.fail(ctx, execErr.Error())
		}
		if err := r.save(ctx); err != nil {
			return r.fail(ctx, err.Error())
		}
	}
	return r.run(ctx, "", true)
}

// loop runs provider iterations until the model stops calling tools.
func (r *runner) loop(ctx context.Context) (*core.RunResult, error) {
	for {
		if r.iterations >= r.optMaxIterations() {
			break
		}
		r.iterations++

		req, err := r.buildRequest(ctx)
		if err != nil {
			return nil, err
		}
		spanStart := time.Now()
		resp, toolCalls, err := r.providerCall(ctx, req)
		span := core.TraceSpan{
			ID: newID("sp"), RunID: r.runID, Name: "llm", Kind: "llm",
			StartedAt: spanStart, DurationMS: time.Since(spanStart).Milliseconds(),
			Status: "ok", Tokens: r.usage,
		}
		if err != nil {
			span.Status = "error"
			if errors.Is(err, context.Canceled) {
				span.Status = "cancelled"
			}
			span.Output = json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		r.emitSpan(span)
		if err != nil {
			return nil, err
		}
		if msg := r.budgetExceeded(); msg != "" {
			return nil, errors.New(msg)
		}

		assistant := core.NewAssistantMessage(resp, toolCalls)
		r.session.Messages = append(r.session.Messages, assistant)
		if err := r.save(ctx); err != nil {
			return nil, err
		}
		r.emitEvent(core.RunEvent{Type: core.EventMessageComplete, RunID: r.runID, Agent: r.agent.Name, Message: &assistant})
		if h := r.agent.Hooks.OnMessage; h != nil {
			h(ctx, assistant)
		}

		if len(toolCalls) == 0 {
			return r.buildResult(core.RunStatusCompleted, "", nil), nil
		}

		// Runaway detection: the same tool called 3 times with identical
		// arguments means the loop is spinning (e.g. a tool that always
		// errors in a way the model never learns from).
		for _, c := range toolCalls {
			sig := c.Name + "\x00" + string(c.Arguments)
			r.callCounts[sig]++
			if r.callCounts[sig] >= 3 {
				return nil, fmt.Errorf("runaway loop detected: tool %s called 3 times with identical arguments", c.Name)
			}
		}

		// Execute the tool calls once. Calls that raise an approval
		// request are blocked (never executed) and pause the run.
		if _, blocked, err := r.executeTurn(ctx, r.callsToPending(toolCalls)); err != nil {
			return nil, err
		} else if len(blocked) > 0 {
			r.session.PendingCalls = append(r.session.PendingCalls, blocked...)
			if err := r.save(ctx); err != nil {
				return nil, err
			}
			return nil, errAwaitingApproval
		}
		if err := r.save(ctx); err != nil {
			return nil, err
		}
	}
	return r.buildResult(core.RunStatusCompleted, "", nil), nil
}

// executeTurn runs all calls in parallel. Completed results are returned
// (and appended to history by the caller); blocked calls are returned
// separately so the run can pause for human approval. PendingToolCall
// entries keep their ApprovalID so a resumed run can inject decisions.
func (r *runner) executeTurn(ctx context.Context, pcs []storage.PendingToolCall) ([]core.ToolResult, []storage.PendingToolCall, error) {
	results := make([]core.ToolResult, len(pcs))
	blocked := []storage.PendingToolCall{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range pcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, pending := r.executeOne(ctx, pcs[i])
			results[i] = res
			if pending != nil {
				mu.Lock()
				blocked = append(blocked, *pending)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// Calls that are paused for human approval must not be cached or
	// recorded as completed: they will execute after the decision.
	blockedIDs := map[string]bool{}
	for _, pc := range blocked {
		blockedIDs[pc.Call.ID] = true
	}
	// Loaded sessions may have a nil ToolCache (toolCache is omitempty
	// in storage): initialize before writing results.
	if r.session.ToolCache == nil {
		r.session.ToolCache = map[string]json.RawMessage{}
	}
	for _, res := range results {
		if res.ID == "" || blockedIDs[res.ID] {
			continue
		}
		content := res.Content
		if res.Error != "" {
			content = json.RawMessage(fmt.Sprintf(`{"error":%q}`, res.Error))
		}
		msg := core.NewToolMessage(res.ID, res.Name, content)
		r.session.Messages = append(r.session.Messages, msg)
		r.session.ToolCache[res.ID] = content
		r.emitEvent(core.RunEvent{Type: core.EventToolResult, RunID: r.runID, Agent: r.agent.Name, ToolResult: &res})
		if h := r.agent.Hooks.OnToolResult; h != nil {
			h(ctx, res)
		}
	}
	return results, blocked, nil
}

// executeOne runs a single tool call. The returned pending call is
// non-nil when the tool requested human approval before any side effect.
func (r *runner) executeOne(ctx context.Context, pc storage.PendingToolCall) (core.ToolResult, *storage.PendingToolCall) {
	call := pc.Call
	res := core.ToolResult{ID: call.ID, Name: call.Name, Content: json.RawMessage(`{}`)}
	// Cached replay (HITL resume safety): already-completed tools run once.
	if cached, ok := r.session.ToolCache[call.ID]; ok {
		res.Content = cached
		r.emitEvent(core.RunEvent{Type: core.EventToolCall, RunID: r.runID, Agent: r.agent.Name, ToolCall: &call})
		return res, nil
	}
	// Rejected by a human decision (explicit false — a missing key means
	// the call is not approval-gated).
	if approved, exists := r.approvals[pc.ApprovalID]; exists && !approved {
		res.Error = "tool call rejected by human"
		res.ApprovalRequired = true
		return res, nil
	}
	tool, ok := r.tools()[call.Name]
	if !ok {
		res.Error = fmt.Sprintf("tool %q not found", call.Name)
		return res, nil
	}
	// Guardrail: denied tools never execute (checked on resume too).
	if slices.Contains(r.agent.DenyTools, call.Name) {
		res.Error = fmt.Sprintf("tool %q is denied by agent policy", call.Name)
		return res, nil
	}
	// Guardrail: tools that always require human approval.
	if pc.ApprovalID == "" && slices.Contains(r.agent.RequireApprovalTools, call.Name) {
		return res, r.requestApproval(call, "Run tool "+call.Name, string(call.Arguments), map[string]any{"tool": call.Name, "policy": "requireApproval"})
	}
	r.emitEvent(core.RunEvent{Type: core.EventToolCall, RunID: r.runID, Agent: r.agent.Name, ToolCall: &call})
	if h := r.agent.Hooks.OnToolCall; h != nil {
		h(ctx, call)
	}
	tc := core.NewToolContext(r.agent.Name, r.runID)
	if pc.ApprovalID != "" {
		// Inject only this call's decision so RequestApproval resolves
		// exactly one approval.
		tc.Approval = map[string]bool{pc.ApprovalID: r.approvals[pc.ApprovalID]}
	}
	tc.Emit = r.emit
	spanStart := time.Now()
	span := core.TraceSpan{ID: newID("sp"), RunID: r.runID, Name: "tool:" + call.Name, Kind: "tool", StartedAt: spanStart, Status: "ok", Input: call.Arguments}
	result, err := tool.Run(ctx, tc, call.Arguments)
	if err != nil {
		var are *core.ApprovalRequiredError
		if errors.As(err, &are) {
			span.Status = "blocked"
			span.DurationMS = time.Since(spanStart).Milliseconds()
			r.emitSpan(span)
			approvalID := pc.ApprovalID
			if approvalID == "" {
				approvalID = newID("ap")
			}
			ap := core.ApprovalRequest{
				ID:        approvalID,
				RunID:     r.runID,
				AgentName: r.agent.Name,
				Action:    are.Action,
				Summary:   are.Summary,
				Context:   are.Context,
				Status:    "pending",
				CreatedAt: time.Now().UTC(),
			}
			r.session.PendingApprovals = append(r.session.PendingApprovals, ap)
			r.agent.registerApproval(approvalID, r.session.ID)
			r.emitEvent(core.RunEvent{Type: core.EventApprovalRequest, RunID: r.runID, Agent: r.agent.Name, Approval: &ap})
			return res, &storage.PendingToolCall{ApprovalID: approvalID, Call: call}
		}
		span.Status = "error"
		span.DurationMS = time.Since(spanStart).Milliseconds()
		span.Output = json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
		r.emitSpan(span)
		res.Error = err.Error()
		return res, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		res.Error = "marshal tool result: " + err.Error()
		return res, nil
	}
	span.DurationMS = time.Since(spanStart).Milliseconds()
	span.Output = data
	r.emitSpan(span)
	res.Content = data
	return res, nil
}

// requestApproval creates a pending HITL approval for a tool call and
// returns the pending call that pauses the run. Used by the
// requireApproval policy (and by tools via RequestApproval).
func (r *runner) requestApproval(call core.ToolCall, action, summary string, ctxValue map[string]any) *storage.PendingToolCall {
	approvalID := newID("ap")
	ap := core.ApprovalRequest{
		ID:        approvalID,
		RunID:     r.runID,
		AgentName: r.agent.Name,
		Action:    action,
		Summary:   summary,
		Context:   ctxValue,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	r.session.PendingApprovals = append(r.session.PendingApprovals, ap)
	r.agent.registerApproval(approvalID, r.session.ID)
	r.emitEvent(core.RunEvent{Type: core.EventApprovalRequest, RunID: r.runID, Agent: r.agent.Name, Approval: &ap})
	r.emitSpan(core.TraceSpan{ID: newID("sp"), RunID: r.runID, Name: "approval:" + call.Name, Kind: "approval", StartedAt: time.Now(), Status: "blocked", Input: call.Arguments})
	return &storage.PendingToolCall{ApprovalID: approvalID, Call: call}
}

// emitSpan records a span and emits it as a trace.span event.
func (r *runner) emitSpan(sp core.TraceSpan) {
	r.spans = append(r.spans, sp)
	r.emitEvent(core.RunEvent{Type: core.EventTraceSpan, RunID: r.runID, Agent: r.agent.Name, Span: &sp})
}

// totalTokens sums the run's input+output usage.
func (r *runner) totalTokens() int {
	if r.usage == nil {
		return 0
	}
	return r.usage.InputTokens + r.usage.OutputTokens
}

// costCents estimates the run's spend from the agent's token prices.
func (r *runner) costCents() float64 {
	if r.agent.Cost == nil || r.usage == nil {
		return 0
	}
	return float64(r.usage.InputTokens)/1000*r.agent.Cost.InputPer1K +
		float64(r.usage.OutputTokens)/1000*r.agent.Cost.OutputPer1K
}

// budgetExceeded reports which budget (tokens or cost) the run has blown,
// or "" when it is still within limits.
func (r *runner) budgetExceeded() string {
	max := r.opts.MaxTotalTokens
	if max == -1 {
		max = 0
	} else if max == 0 {
		max = r.agent.MaxTotalTokens
	}
	if max > 0 && r.totalTokens() >= max {
		return fmt.Sprintf("token budget exceeded (%d >= %d tokens): run stopped", r.totalTokens(), max)
	}
	if r.agent.MaxCostCents > 0 && r.costCents() >= r.agent.MaxCostCents {
		return fmt.Sprintf("cost budget exceeded (%.2f >= %.2f cents): run stopped", r.costCents(), r.agent.MaxCostCents)
	}
	return ""
}

// providerCall invokes the model with retry and streaming.
func (r *runner) providerCall(ctx context.Context, req llm.ChatRequest) (string, []core.ToolCall, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 300 * time.Millisecond):
			}
		}
		content, calls, err := r.providerOnce(ctx, req)
		if err == nil {
			return content, calls, nil
		}
		lastErr = err
		if !core.Retryable(err) {
			return "", nil, err
		}
	}
	return "", nil, lastErr
}

func (r *runner) providerOnce(ctx context.Context, req llm.ChatRequest) (string, []core.ToolCall, error) {
	s, err := r.agent.Provider.Stream(ctx, req)
	if err != nil {
		return "", nil, err
	}
	defer s.Close()
	var content strings.Builder
	var calls []core.ToolCall
	for {
		chunk, err := s.Next()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", nil, core.NewError(core.KindInterrupt, err.Error(), err)
			}
			break
		}
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			r.emitEvent(core.RunEvent{Type: core.EventMessageDelta, RunID: r.runID, Agent: r.agent.Name, Delta: chunk.Content})
		}
		if len(chunk.ToolCalls) > 0 {
			calls = chunk.ToolCalls
		}
		if chunk.Usage != nil {
			if r.usage == nil {
				r.usage = &core.Usage{}
			}
			r.usage.InputTokens += chunk.Usage.InputTokens
			r.usage.OutputTokens += chunk.Usage.OutputTokens
		}
	}
	if r.agent.Provider == nil {
		return "", nil, core.NewError(core.KindProvider, "agent has no provider")
	}
	return content.String(), calls, nil
}

// buildRequest assembles the provider request (system + history +
// knowledge context).
func (r *runner) buildRequest(ctx context.Context) (llm.ChatRequest, error) {
	messages := []core.Message{}
	var kchunks []string
	system := ""
	if r.agent.Instructions != "" || r.agent.Knowledge != nil {
		system = r.agent.Instructions
		if r.agent.Knowledge != nil {
			// Retrieve the most relevant knowledge chunks for the last
			// user message.
			query := ""
			for i := len(r.session.Messages) - 1; i >= 0; i-- {
				if r.session.Messages[i].Role == core.RoleUser {
					query = r.session.Messages[i].Text()
					break
				}
			}
			if query != "" {
				chunks, err := r.agent.Knowledge.Query(ctx, query, 4)
				if err == nil && len(chunks) > 0 {
					var sb strings.Builder
					sb.WriteString("\n\nKnowledge base:\n")
					for _, c := range chunks {
						kchunks = append(kchunks, c.Text)
						sb.WriteString("- " + c.Text + "\n")
					}
					system += sb.String()
				}
			}
		}
		if system != "" {
			messages = append(messages, core.Message{Role: core.RoleSystem, Content: system, CreatedAt: time.Now().UTC()})
		}
	}
	history := r.session.Messages
	if r.mem != nil && r.mem.Strategy != nil {
		history = r.mem.Strategy.Trim(history)
	}
	history = sanitizeHistory(history)
	history = capToolResults(history)
	messages = append(messages, history...)
	req := llm.ChatRequest{
		Model:        r.agent.Provider.Model(),
		Messages:     messages,
		Tools:        r.agent.Tools,
		Temperature:  r.opts.Temperature,
		MaxTokens:    r.opts.MaxTokens,
		Stop:         r.opts.Stop,
		ResponseSchema: r.opts.ResponseSchema,
		ResponseSchemaName: r.opts.ResponseSchemaName,
	}
	if req.Temperature == nil {
		req.Temperature = r.agent.Temperature
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = r.agent.MaxTokens
	}
	if len(req.Stop) == 0 {
		req.Stop = r.agent.Stop
	}
	// Record what the model actually saw, so the run trace and eval
	// contextContains assertions can audit it.
	r.lastContext = &core.RunContext{
		SystemPrompt: system,
		Knowledge:    kchunks,
		HistorySent:  len(history),
		HistoryTotal: len(r.session.Messages),
	}
	return req, nil
}

// maxToolResultRunes caps how much of a single tool result is sent back
// to the model. A huge result (e.g. an http_fetch of a large page, now or
// from an older session) would otherwise blow the model context window.
const maxToolResultRunes = 32 << 10

// sanitizeHistory makes history safe for OpenAI-compatible providers,
// which reject an assistant tool_calls message that has no tool response
// for every id. Runs that abort after emitting calls (runaway guard,
// budget, crash, pending HITL) can leave such dangling messages; a new
// run appends its own user message after them, so they are not always
// the tail. The sanitizer walks the full history and drops any
// unanswered assistant tool_calls message together with the tool
// responses that belonged to it (they would otherwise be orphaned).
func sanitizeHistory(in []core.Message) []core.Message {
	if len(in) < 2 {
		return in
	}
	drop := map[int]bool{} // indices to drop
	for i := 0; i < len(in); i++ {
		m := in[i]
		if m.Role != core.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		ids := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			ids[tc.ID] = true
		}
		answeredAt := map[int]string{}
		for j := i + 1; j < len(in); j++ {
			if in[j].Role == core.RoleTool && ids[in[j].ToolCallID] {
				answeredAt[j] = in[j].ToolCallID
				delete(ids, in[j].ToolCallID)
				if len(ids) == 0 {
					break
				}
			}
		}
		if len(ids) == 0 {
			continue // every call answered
		}
		drop[i] = true
		for j := range answeredAt {
			drop[j] = true
		}
	}
	if len(drop) == 0 {
		return in
	}
	out := make([]core.Message, 0, len(in)-len(drop))
	for i, m := range in {
		if !drop[i] {
			out = append(out, m)
		}
	}
	return out
}

// capToolResults returns a copy of history in which oversized tool
// messages are truncated at the request level. Storage keeps the full
// payload; only the provider request is capped.
func capToolResults(in []core.Message) []core.Message {
	copied := false
	var out []core.Message
	for i := range in {
		m := in[i]
		if m.Role == core.RoleTool && len(m.Content) > maxToolResultRunes {
			if !copied {
				out = append(out, in[:i]...)
				copied = true
			}
			runes := []rune(m.Content)
			if len(runes) > maxToolResultRunes {
				runes = runes[:maxToolResultRunes]
			}
			m.Content = string(runes) + "\n…[truncated tool result]"
			out = append(out, m)
		} else if copied {
			out = append(out, m)
		}
	}
	if !copied {
		return in
	}
	return out
}

func (r *runner) tools() map[string]*core.Tool {
	return core.ToolsByName(r.agent.Tools)
}

func (r *runner) optMaxIterations() int {
	if r.opts.MaxIterations > 0 {
		return r.opts.MaxIterations
	}
	if r.agent.MaxIterations > 0 {
		return r.agent.MaxIterations
	}
	return 8
}

func (r *runner) callsToPending(calls []core.ToolCall) []storage.PendingToolCall {
	out := make([]storage.PendingToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, storage.PendingToolCall{Call: c})
	}
	return out
}

// loadSession loads or creates the backing session.
func (r *runner) loadSession(ctx context.Context) (*storage.Session, error) {
	if r.mem == nil {
		if r.ephemeral == nil {
			r.ephemeral = storage.NewSession(r.runID, r.agent.Name, r.opts.UserID)
		}
		return r.ephemeral, nil
	}
	sessionID := r.opts.SessionID
	if sessionID == "" {
		sessionID = r.mem.SessionID
	}
	if sessionID == "" {
		sessionID = newID("session")
	}
	r.opts.SessionID = sessionID
	m := memory.New(r.mem.Store, memory.WithSessionID(sessionID), memory.WithUserID(r.opts.UserID), memory.WithStrategy(r.mem.Strategy))
	r.mem = m
	sess, err := m.Session(ctx)
	if err != nil {
		return nil, err
	}
	sess.AgentName = r.agent.Name
	sess.UserID = r.opts.UserID
	if sess.Metadata == nil {
		sess.Metadata = map[string]string{}
	}
	sess.Metadata["lastRunID"] = r.runID
	return sess, nil
}

func (r *runner) save(ctx context.Context) error {
	if r.mem == nil {
		return nil
	}
	r.session.UpdatedAt = time.Now().UTC()
	return r.mem.Store.Save(ctx, r.session)
}

func (r *runner) buildResult(status core.RunStatus, out string, err error) *core.RunResult {
	if out == "" && err == nil {
		for i := len(r.session.Messages) - 1; i >= 0; i-- {
			if r.session.Messages[i].Role == core.RoleAssistant {
				out = r.session.Messages[i].Text()
				break
			}
		}
	}
	res := &core.RunResult{
		RunID:     r.runID,
		Status:    status,
		Output:    out,
		Messages:  r.session.Messages,
		Approvals: r.session.PendingApprovals,
		Usage:     r.usage,
		DurationMS: time.Since(r.started).Milliseconds(),
		Context:   r.lastContext,
		Metadata: map[string]any{
			"agent":      r.agent.Name,
			"iterations": r.iterations,
			"sessionId":  r.session.ID,
		},
	}
	if err != nil {
		res.Error = err.Error()
	}
	r.emitEvent(core.RunEvent{Type: core.EventRunMetrics, RunID: r.runID, Agent: r.agent.Name, Metrics: &core.RunMetrics{
		Iterations: r.iterations,
		Tokens:     r.usage,
		CostCents:  r.costCents(),
		DurationMS: time.Since(r.started).Milliseconds(),
		Status:     string(status),
	}})
	r.emitEvent(core.RunEvent{Type: core.EventRunComplete, RunID: r.runID, Agent: r.agent.Name, Result: res})
	if h := r.agent.Hooks.OnFinish; h != nil {
		h(context.Background(), res)
	}
	return res
}

func (r *runner) fail(ctx context.Context, msg string) (*core.RunResult, error) {
	e := core.NewError(core.KindAgent, msg)
	r.emitEvent(core.RunEvent{Type: core.EventRunError, RunID: r.runID, Agent: r.agent.Name, Error: e.Error()})
	res := r.buildResult(core.RunStatusFailed, "", e)
	return res, e
}
