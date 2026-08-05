package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/knowledge"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/storage"
	"github.com/nemo715/Ernest/internal/vector"
)

func mockScripted(t *testing.T, script []llm.MockTurn, def llm.MockTurn) *llm.MockProvider {
	t.Helper()
	return llm.NewMock(llm.MockConfig{Script: script, Default: def})
}

func TestChatBasic(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{{Content: "Hello there", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("basic", p)
	res, err := a.Chat(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Output != "Hello there" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.RunID == "" {
		t.Fatal("run id missing")
	}
	if p.CallCount() != 1 {
		t.Fatalf("call count = %d", p.CallCount())
	}
}

func TestChatToolLoop(t *testing.T) {
	call := core.ToolCall{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"6*7"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "The answer is 42", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("tools", p)
	a.Tools = []*core.Tool{core.Calculator}
	res, err := a.Chat(context.Background(), "6*7?")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "The answer is 42" {
		t.Fatalf("output = %q", res.Output)
	}
	if p.CallCount() != 2 {
		t.Fatalf("expected 2 model calls, got %d", p.CallCount())
	}
	// Tool result must be in history with the right correlation id.
	found := false
	for _, m := range res.Messages {
		if m.Role == core.RoleTool && m.ToolCallID == "c1" && strings.Contains(m.Content, "42") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool result missing from history: %+v", res.Messages)
	}
}

func TestChatUnknownTool(t *testing.T) {
	call := core.ToolCall{ID: "c9", Name: "does_not_exist", Arguments: []byte(`{}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "I could not do that", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("tools", p)
	a.Tools = []*core.Tool{core.Calculator}
	res, err := a.Chat(context.Background(), "do it")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range res.Messages {
		if m.Role == core.RoleTool && strings.Contains(m.Content, "not found") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool not-found error missing: %+v", res.Messages)
	}
}

func TestStreamEventOrder(t *testing.T) {
	call := core.ToolCall{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"1+1"}`)}
	p := llm.NewMock(llm.MockConfig{
		Stream: true, // character deltas
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "Two", FinishReason: "stop"},
		},
	})
	a := New("stream", p)
	a.Tools = []*core.Tool{core.Calculator}

	ch, err := a.Stream(context.Background(), "1+1?")
	if err != nil {
		t.Fatal(err)
	}
	var types []core.EventType
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if len(types) < 6 {
		t.Fatalf("too few events: %v", types)
	}
	if types[0] != core.EventRunStart {
		t.Fatalf("first event = %s", types[0])
	}
	// deltas for turn 1 content (none, empty content) then message.complete
	firstComplete := -1
	lastComplete := -1
	toolCallIdx := -1
	toolResultIdx := -1
	runCompleteIdx := -1
	for i, ty := range types {
		switch ty {
		case core.EventMessageComplete:
			if firstComplete < 0 {
				firstComplete = i
			}
			lastComplete = i
		case core.EventToolCall:
			toolCallIdx = i
		case core.EventToolResult:
			toolResultIdx = i
		case core.EventRunComplete:
			runCompleteIdx = i
		}
	}
	if firstComplete < 0 || toolCallIdx < 0 || toolResultIdx < 0 || runCompleteIdx < 0 {
		t.Fatalf("missing event kinds: %v", types)
	}
	if toolCallIdx < firstComplete+1 || toolResultIdx < toolCallIdx || toolCallIdx > lastComplete {
		t.Fatalf("tool events out of order: %v", types)
	}
	if runCompleteIdx != len(types)-1 {
		t.Fatalf("run.complete must be last: %v", types)
	}
}

func TestMaxIterations(t *testing.T) {
	// Distinct calls each iteration (identical repeats would trip the
	// runaway detector); the cap must stop the loop at 3.
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"1"}`)}}, FinishReason: "tool_calls"},
		{ToolCalls: []core.ToolCall{{ID: "c2", Name: "calculator", Arguments: []byte(`{"expression":"1+1"}`)}}, FinishReason: "tool_calls"},
		{ToolCalls: []core.ToolCall{{ID: "c3", Name: "calculator", Arguments: []byte(`{"expression":"1+1+1"}`)}}, FinishReason: "tool_calls"},
	}, llm.MockTurn{ToolCalls: []core.ToolCall{{ID: "c4", Name: "calculator", Arguments: []byte(`{"expression":"1+1+1+1"}`)}}, FinishReason: "tool_calls"})
	a := New("looper", p)
	a.Tools = []*core.Tool{core.Calculator}
	a.MaxIterations = 3
	res, err := a.Chat(context.Background(), "keep going")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Metadata["iterations"] != 3 {
		t.Fatalf("iterations = %v", res.Metadata["iterations"])
	}
}

// ---------------------------------------------------------------------------
// HITL
// ---------------------------------------------------------------------------

type emailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
}

func hitlAgent(t *testing.T, sent *int) (*Agent, string) {
	t.Helper()
	call := core.ToolCall{ID: "e1", Name: "send_email", Arguments: []byte(`{"to":"a@b.c","subject":"hello"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "Email sent", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("hitl", p)
	a.Store = storage.NewInMemoryStore()
	a.Tools = []*core.Tool{core.MustTool[emailArgs]("send_email", "Send an email", func(ctx context.Context, tc *core.ToolContext, args emailArgs) (any, error) {
		if err := tc.RequestApproval("send_email", "Send email to "+args.To, map[string]any{"to": args.To}); err != nil {
			return nil, err
		}
		*sent++
		return map[string]any{"sent": true, "to": args.To}, nil
	})}
	return a, "sess-hitl"
}

func TestHITLApprovalFlow(t *testing.T) {
	sent := 0
	a, sessionID := hitlAgent(t, &sent)
	ctx := context.Background()

	res, err := a.Chat(ctx, "email a@b.c", RunOptions{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s, want awaiting_approval", res.Status)
	}
	if len(res.Approvals) != 1 || res.Approvals[0].Action != "send_email" {
		t.Fatalf("approvals = %+v", res.Approvals)
	}
	if sent != 0 {
		t.Fatalf("tool ran before approval: sent=%d", sent)
	}
	ap := res.Approvals[0]

	// Approval is visible through the API.
	pending, err := a.PendingApprovals(ctx, sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	// Approve -> tool executes exactly once, run completes.
	res2, err := a.Resume(ctx, core.ApprovalDecision{ApprovalID: ap.ID, Approved: true, Note: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != core.RunStatusCompleted {
		t.Fatalf("resumed status = %s", res2.Status)
	}
	if sent != 1 {
		t.Fatalf("tool executed %d times, want 1", sent)
	}
	if res2.Output != "Email sent" {
		t.Fatalf("output = %q", res2.Output)
	}
	// Approval resolved in the session.
	pending2, _ := a.PendingApprovals(ctx, sessionID)
	if len(pending2) != 0 {
		t.Fatalf("approval should be resolved: %+v", pending2)
	}
}

func TestHITLRejectionFlow(t *testing.T) {
	sent := 0
	a, sessionID := hitlAgent(t, &sent)
	ctx := context.Background()

	res, err := a.Chat(ctx, "email a@b.c", RunOptions{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s", res.Status)
	}

	res2, err := a.Resume(ctx, core.ApprovalDecision{ApprovalID: res.Approvals[0].ID, Approved: false, Note: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res2.Status)
	}
	if sent != 0 {
		t.Fatalf("rejected tool must not run, sent=%d", sent)
	}
	// The model is told the rejection happened.
	rejected := false
	for _, m := range res2.Messages {
		if m.Role == core.RoleTool && strings.Contains(m.Content, "rejected") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("rejection feedback missing: %+v", res2.Messages)
	}
}

func TestResumeUnknownApproval(t *testing.T) {
	a, _ := hitlAgent(t, new(int))
	_, err := a.Resume(context.Background(), core.ApprovalDecision{ApprovalID: "nope", Approved: true})
	if err == nil {
		t.Fatal("unknown approval id must fail")
	}
}

func TestStreamResumeUnknownApproval(t *testing.T) {
	a, _ := hitlAgent(t, new(int))
	_, err := a.StreamResume(context.Background(), core.ApprovalDecision{ApprovalID: "nope", Approved: true})
	if err == nil {
		t.Fatal("unknown approval id must fail")
	}
}

func TestStreamResumeWithoutStoreFailsFast(t *testing.T) {
	// Regression: an ephemeral run (no Memory/Store) can pause for HITL
	// but has no session to replay from on resume. StreamResume must
	// return the error up front, not a stream that closes without events.
	call := core.ToolCall{ID: "e1", Name: "send_email", Arguments: []byte(`{}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
	}, llm.MockTurn{})
	a := New("ephemeral", p)
	a.Tools = []*core.Tool{core.MustTool[emailArgs]("send_email", "Send an email", func(ctx context.Context, tc *core.ToolContext, args emailArgs) (any, error) {
		return nil, tc.RequestApproval("send_email", "send?", nil)
	})}

	res, err := a.Chat(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s, want awaiting_approval", res.Status)
	}

	_, err = a.StreamResume(context.Background(), core.ApprovalDecision{ApprovalID: res.Approvals[0].ID, Approved: true})
	if err == nil {
		t.Fatal("resume of an ephemeral run must fail with a store error")
	}
	if !strings.Contains(err.Error(), "no Memory or Store") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Memory + knowledge
// ---------------------------------------------------------------------------

func TestMemoryPersistence(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{
		{Content: "hello one", FinishReason: "stop"},
		{Content: "hello two", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("mem", p)
	a.Store = storage.NewInMemoryStore()
	ctx := context.Background()

	if _, err := a.Chat(ctx, "first message", RunOptions{SessionID: "s-mem"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(ctx, "second message", RunOptions{SessionID: "s-mem"}); err != nil {
		t.Fatal(err)
	}
	// The second provider call must see both user messages.
	reqs := p.Requests
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	users := 0
	for _, m := range reqs[1].Messages {
		if m.Role == core.RoleUser {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("second request must include both user messages, got %d", users)
	}
}

func TestKnowledgeInjection(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{{Content: "sure", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("know", p)
	a.Store = storage.NewInMemoryStore()
	kb := knowledge.New(vector.NewInMemoryStore(), p, knowledge.ChunkOptions{})
	if _, err := kb.AddText(context.Background(), "Ernest is the fastest agent framework with memory and tools.", nil); err != nil {
		t.Fatal(err)
	}
	a.Knowledge = kb

	if _, err := a.Chat(context.Background(), "what is ernest?"); err != nil {
		t.Fatal(err)
	}
	system := ""
	for _, m := range p.Requests[0].Messages {
		if m.Role == core.RoleSystem {
			system = m.Content
		}
	}
	if !strings.Contains(system, "Knowledge base") || !strings.Contains(system, "Ernest is the fastest") {
		t.Fatalf("knowledge not injected: %q", system)
	}
}

func TestSkipMemoryEphemeral(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("ephemeral", p) // no Store, no Memory
	res, err := a.Chat(context.Background(), "hi", RunOptions{SkipMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestNoMemoryDefaultsToEphemeral(t *testing.T) {
	// Zero-config agent: no Store, no Memory — runs are ephemeral.
	p := mockScripted(t, []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("bare", p)
	res, err := a.Chat(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Hooks + options
// ---------------------------------------------------------------------------

func TestHooks(t *testing.T) {
	call := core.ToolCall{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"2*2"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "4", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("hooks", p)
	a.Tools = []*core.Tool{core.Calculator}

	started, msgs, calls, results, finished := 0, 0, 0, 0, 0
	a.Hooks = Hooks{
		OnStart:      func(ctx context.Context, input string) { started++ },
		OnMessage:    func(ctx context.Context, msg core.Message) { msgs++ },
		OnToolCall:   func(ctx context.Context, call core.ToolCall) { calls++ },
		OnToolResult: func(ctx context.Context, res core.ToolResult) { results++ },
		OnFinish:     func(ctx context.Context, res *core.RunResult) { finished++ },
	}
	if _, err := a.Chat(context.Background(), "2*2?"); err != nil {
		t.Fatal(err)
	}
	if started != 1 || msgs != 2 || calls != 1 || results != 1 || finished != 1 {
		t.Fatalf("hooks fired wrong: start=%d msg=%d call=%d result=%d finish=%d", started, msgs, calls, results, finished)
	}
}

func TestRunOptionsPassThrough(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("opts", p)
	temp := 0.7
	schema, err := core.SchemaFromStruct(struct {
		Answer string `json:"answer"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Chat(context.Background(), "hi", RunOptions{
		Temperature:        &temp,
		MaxTokens:          99,
		Stop:               []string{"END"},
		ResponseSchema:     schema,
		ResponseSchemaName: "answer_schema",
		SkipMemory:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := p.Requests[0]
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Fatalf("temperature = %v", req.Temperature)
	}
	if req.MaxTokens != 99 || len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Fatalf("opts wrong: %+v", req)
	}
	if req.ResponseSchema == nil || req.ResponseSchemaName != "answer_schema" {
		t.Fatalf("response schema missing")
	}
}

func TestClearMemory(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{
		{Content: "one", FinishReason: "stop"},
		{Content: "two", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("clear", p)
	a.Store = storage.NewInMemoryStore()
	ctx := context.Background()
	if _, err := a.Chat(ctx, "first", RunOptions{SessionID: "s-clear"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ClearMemory(ctx, "s-clear"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(ctx, "second", RunOptions{SessionID: "s-clear"}); err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range p.Requests[1].Messages {
		if m.Role == core.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("cleared session must have 1 user message, got %d", users)
	}
}

// ---------------------------------------------------------------------------
// Guardrails (M3): budgets, policies, redaction
// ---------------------------------------------------------------------------

func TestDenyToolPolicy(t *testing.T) {
	call := core.ToolCall{ID: "d1", Name: "calculator", Arguments: []byte(`{"expression":"1"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "Denied", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("deny", p)
	a.Tools = []*core.Tool{core.Calculator}
	a.DenyTools = []string{"calculator"}
	res, err := a.Chat(context.Background(), "calc")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	found := false
	for _, m := range res.Messages {
		if m.Role == core.RoleTool && strings.Contains(m.Content, "denied by agent policy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("denied tool must surface an error result: %+v", res.Messages)
	}
}

func TestRequireApprovalPolicy(t *testing.T) {
	// The tool itself never calls RequestApproval; the policy gates it.
	sent := 0
	call := core.ToolCall{ID: "r1", Name: "send_email", Arguments: []byte(`{"to":"a@b.c","subject":"hi"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "Done", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("policy", p)
	a.Store = storage.NewInMemoryStore()
	a.Tools = []*core.Tool{core.MustTool[emailArgs]("send_email", "Send an email", func(ctx context.Context, tc *core.ToolContext, args emailArgs) (any, error) {
		sent++
		return map[string]any{"sent": true}, nil
	})}
	a.RequireApprovalTools = []string{"send_email"}
	ctx := context.Background()
	res, err := a.Chat(ctx, "send", RunOptions{SessionID: "s-policy"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s", res.Status)
	}
	if len(res.Approvals) != 1 || res.Approvals[0].Context["policy"] != "requireApproval" {
		t.Fatalf("approvals = %+v", res.Approvals)
	}
	if sent != 0 {
		t.Fatal("gated tool must not execute before approval")
	}
	// Approve: the tool runs and the run completes.
	if _, err := a.Resume(ctx, core.ApprovalDecision{ApprovalID: res.Approvals[0].ID, Approved: true}); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("tool executed %d times after approval", sent)
	}
}

func TestRunawayLoopDetection(t *testing.T) {
	// The provider keeps issuing the same tool call forever: the loop is
	// spinning (the tool always errors in a way the model never learns
	// from). Detected after 3 identical calls.
	call := core.ToolCall{ID: "r1", Name: "calculator", Arguments: []byte(`{"expression":"1/0"}`)}
	p := mockScripted(t, nil, llm.MockTurn{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"})
	a := New("runaway", p)
	a.Tools = []*core.Tool{core.Calculator}
	res, err := a.Chat(context.Background(), "go")
	if err == nil {
		t.Fatalf("expected runaway error, got %+v", res)
	}
	if !strings.Contains(err.Error(), "runaway loop") {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != core.RunStatusFailed {
		t.Fatalf("status = %s", res.Status)
	}
	if p.CallCount() != 3 {
		t.Fatalf("expected 3 model calls before detection, got %d", p.CallCount())
	}
}

func TestTokenBudgetEnforced(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{
		{Content: "a", FinishReason: "stop", Usage: &core.Usage{InputTokens: 100, OutputTokens: 10}},
	}, llm.MockTurn{})
	a := New("budget", p)
	a.MaxTotalTokens = 50
	res, err := a.Chat(context.Background(), "hi")
	if err == nil {
		t.Fatalf("expected budget error, got %+v", res)
	}
	if !strings.Contains(err.Error(), "token budget exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != core.RunStatusFailed {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestRedactInput(t *testing.T) {
	p := mockScripted(t, []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}, llm.MockTurn{})
	a := New("redact", p)
	a.RedactPatterns = []string{`secret-\d+`}
	res, err := a.Chat(context.Background(), "my card is secret-1234", RunOptions{SkipMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) == 0 || res.Messages[0].Role != core.RoleUser {
		t.Fatalf("messages = %+v", res.Messages)
	}
	if strings.Contains(res.Messages[0].Text(), "secret-1234") || !strings.Contains(res.Messages[0].Text(), "[REDACTED]") {
		t.Fatalf("user message not redacted: %q", res.Messages[0].Text())
	}
}

func TestTraceSpanEventsEmitted(t *testing.T) {
	call := core.ToolCall{ID: "t1", Name: "calculator", Arguments: []byte(`{"expression":"2+2"}`)}
	p := mockScripted(t, []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "4", FinishReason: "stop"},
	}, llm.MockTurn{})
	a := New("trace", p)
	a.Tools = []*core.Tool{core.Calculator}
	ch, err := a.Stream(context.Background(), "2+2?")
	if err != nil {
		t.Fatal(err)
	}
	var spans []core.TraceSpan
	var metrics *core.RunMetrics
	for ev := range ch {
		if ev.Span != nil {
			spans = append(spans, *ev.Span)
		}
		if ev.Metrics != nil {
			metrics = ev.Metrics
		}
	}
	if len(spans) < 3 {
		t.Fatalf("expected >=3 spans (llm, tool, llm), got %d", len(spans))
	}
	if spans[0].Kind != "llm" || spans[0].Name != "llm" || spans[0].Status != "ok" {
		t.Fatalf("first span = %+v", spans[0])
	}
	toolSpan := false
	for _, sp := range spans {
		if sp.Kind == "tool" && sp.Name == "tool:calculator" && len(sp.Output) > 0 {
			toolSpan = true
		}
	}
	if !toolSpan {
		t.Fatalf("tool span missing: %+v", spans)
	}
	if metrics == nil || metrics.Status != string(core.RunStatusCompleted) {
		t.Fatalf("run metrics missing: %+v", metrics)
	}
}
