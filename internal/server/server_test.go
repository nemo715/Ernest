package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/audit"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/eval"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/storage"
)

type emailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
}

// newTestServer boots a server with two agents:
//   - "assistant": scripted content agent (Stream deltas on)
//   - "hitl":      send_email tool gated by RequestApproval
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	assistant := agent.New("assistant", llm.NewMock(llm.MockConfig{
		Stream: true,
		Delay:  20 * time.Millisecond, // slow enough for interrupt tests
		Script: []llm.MockTurn{
			{Content: "Hello world", FinishReason: "stop"},
			{Content: "You are the best", FinishReason: "stop"},
			{Content: "nothing much", FinishReason: "stop"},
		},
	}))
	assistant.Instructions = "You are a helpful assistant."

	call := core.ToolCall{ID: "e1", Name: "send_email", Arguments: []byte(`{"to":"a@b.c","subject":"hello"}`)}
	hitl := agent.New("hitl", llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "Email sent", FinishReason: "stop"},
		},
	}))
	hitl.Store = storage.NewInMemoryStore()
	hitl.Tools = []*core.Tool{core.MustTool[emailArgs]("send_email", "Send an email", func(ctx context.Context, tc *core.ToolContext, args emailArgs) (any, error) {
		if err := tc.RequestApproval("send_email", "Send email to "+args.To, map[string]any{"to": args.To}); err != nil {
			return nil, err
		}
		return map[string]any{"sent": true}, nil
	})}

	aud := audit.NewWithMax(100)
	srv, err := New(Options{Agents: []*agent.Agent{assistant, hitl}, Audit: aud})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts
}

func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/chat"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "test done") })
	return c
}

func wsSend(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func wsReadEvent(t *testing.T, c *websocket.Conn) core.RunEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ev core.RunEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("frame is not a RunEvent: %s", data)
	}
	return ev
}

func wsReadJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWSReadyFrame(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	ready := wsReadJSON(t, c)
	if ready["type"] != "ready" {
		t.Fatalf("first frame = %v", ready)
	}
	agents, _ := ready["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %v", agents)
	}
}

func TestWSChatFullFlow(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	_ = wsReadJSON(t, c) // ready

	wsSend(t, c, map[string]any{"type": "chat", "agent": "assistant", "input": "hi"})
	var types []core.EventType
	var runID string
	for {
		ev := wsReadEvent(t, c)
		types = append(types, ev.Type)
		if ev.RunID != "" {
			runID = ev.RunID
		}
		if ev.Type == core.EventRunComplete {
			break
		}
	}
	// run.start ... deltas ... message.complete ... trace.span ... run.metrics ... run.complete
	hasDelta, hasSpan, hasMetrics := false, false, false
	for _, ty := range types {
		switch ty {
		case core.EventMessageDelta:
			hasDelta = true
		case core.EventTraceSpan:
			hasSpan = true
		case core.EventRunMetrics:
			hasMetrics = true
		}
	}
	if types[0] != core.EventRunStart || types[len(types)-1] != core.EventRunComplete {
		t.Fatalf("event order: %v", types)
	}
	if !hasDelta || !hasSpan || !hasMetrics {
		t.Fatalf("missing realtime events: %v", types)
	}
	if runID == "" {
		t.Fatal("run id missing")
	}

	// The trace must be queryable.
	resp, err := http.Get(ts.URL + "/api/runs/" + runID + "/trace")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trace status = %d", resp.StatusCode)
	}
	var tr struct {
		Spans []core.TraceSpan `json:"spans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Spans) == 0 || tr.Spans[0].Name != "llm" {
		t.Fatalf("spans = %+v", tr.Spans)
	}
}

func TestWSInterrupt(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	_ = wsReadJSON(t, c) // ready

	wsSend(t, c, map[string]any{"type": "chat", "agent": "assistant", "input": "long"})
	// Wait for the first delta, then interrupt mid-stream.
	for {
		ev := wsReadEvent(t, c)
		if ev.Type == core.EventMessageDelta {
			break
		}
		if ev.Type == core.EventRunComplete {
			t.Fatal("run completed before any delta")
		}
	}
	wsSend(t, c, map[string]any{"type": "interrupt"})

	failed := false
	for {
		ev := wsReadEvent(t, c)
		if ev.Type == core.EventRunError {
			if !strings.Contains(ev.Error, "interrupted") {
				t.Fatalf("run.error = %q", ev.Error)
			}
			failed = true
		}
		if ev.Type == core.EventRunComplete {
			if ev.Result == nil || ev.Result.Status != core.RunStatusFailed {
				t.Fatalf("run.complete after interrupt = %+v", ev.Result)
			}
			break
		}
	}
	if !failed {
		t.Fatal("expected run.error after interrupt")
	}
}

func TestWSApproveFlow(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	_ = wsReadJSON(t, c) // ready

	wsSend(t, c, map[string]any{"type": "chat", "agent": "hitl", "input": "email a@b.c", "sessionId": "s-ws"})
	var approvalID string
	for {
		ev := wsReadEvent(t, c)
		if ev.Type == core.EventApprovalRequest {
			approvalID = ev.Approval.ID
		}
		if ev.Type == core.EventRunComplete {
			break // awaiting_approval: the connection is free again
		}
	}
	if approvalID == "" {
		t.Fatal("no approval id")
	}

	wsSend(t, c, map[string]any{"type": "approve", "agent": "hitl", "approvalId": approvalID, "approved": true})
	approved, toolRan, completed := false, false, false
	for {
		ev := wsReadEvent(t, c)
		switch ev.Type {
		case core.EventApprovalResolved:
			approved = ev.Approval.Status == "approved"
		case core.EventToolResult:
			toolRan = true
		case core.EventRunComplete:
			completed = ev.Result.Status == core.RunStatusCompleted
		}
		if ev.Type == core.EventRunComplete {
			break
		}
	}
	if !approved || !toolRan || !completed {
		t.Fatalf("approve flow: approved=%v toolRan=%v completed=%v", approved, toolRan, completed)
	}
}

func TestWSErrors(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	_ = wsReadJSON(t, c) // ready

	wsSend(t, c, map[string]any{"type": "chat", "agent": "nope", "input": "hi"})
	errFrame := wsReadJSON(t, c)
	if errFrame["type"] != "error" || !strings.Contains(errFrame["error"].(string), "unknown agent") {
		t.Fatalf("frame = %v", errFrame)
	}

	wsSend(t, c, map[string]any{"type": "bogus"})
	errFrame = wsReadJSON(t, c)
	if errFrame["type"] != "error" {
		t.Fatalf("frame = %v", errFrame)
	}
}

func TestAuditRoute(t *testing.T) {
	ts := newTestServer(t)
	c := dialWS(t, ts)
	_ = wsReadJSON(t, c) // ready

	// Trigger a tool call + approval decision.
	wsSend(t, c, map[string]any{"type": "chat", "agent": "hitl", "input": "email a@b.c", "sessionId": "s-audit"})
	var approvalID string
	for {
		ev := wsReadEvent(t, c)
		if ev.Type == core.EventApprovalRequest {
			approvalID = ev.Approval.ID
		}
		if ev.Type == core.EventRunComplete {
			break // awaiting_approval
		}
	}
	if approvalID == "" {
		t.Fatal("no approval id")
	}
	wsSend(t, c, map[string]any{"type": "approve", "agent": "hitl", "approvalId": approvalID, "approved": true})
	for {
		ev := wsReadEvent(t, c)
		if ev.Type == core.EventRunComplete {
			break
		}
	}

	resp, err := http.Get(ts.URL + "/api/audit")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit status = %d", resp.StatusCode)
	}
	var entries []audit.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"tool.call", "approval.decided", "run.complete"} {
		if !kinds[want] {
			t.Fatalf("audit missing %q (have %v)", want, kinds)
		}
	}
}

func TestSSEAlsoCapturesTrace(t *testing.T) {
	ts := newTestServer(t)
	body := strings.NewReader(fmt.Sprintf(`{"agent":"assistant","input":"hi","sessionId":"s-sse"}`))
	resp, err := http.Post(ts.URL+"/api/chat", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// Consume the SSE stream.
	runID := ""
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev core.RunEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		runID = ev.RunID
		if ev.Type == core.EventRunComplete {
			break
		}
	}
	if runID == "" {
		t.Fatal("no run id from SSE")
	}
	r2, err := http.Get(ts.URL + "/api/runs/" + runID + "/trace")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("trace status = %d", r2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// failures feed (the production signal behind `ernest eval --learn`)
// ---------------------------------------------------------------------------

// newFlakyServer boots a server with an agent that always fails (the
// runaway-loop guard: 3 identical calculator calls -> "runaway loop
// detected" -> failed status), a control agent that completes fine,
// and a failures feed file.
func newFlakyServer(t *testing.T, feed string) *httptest.Server {
	t.Helper()
	call := core.ToolCall{ID: "f1", Name: "calculator", Arguments: []byte(`{"expression":"1+1"}`)}
	flaky := agent.New("flaky", llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		},
	}))
	flaky.Tools = []*core.Tool{core.Calculator}
	ok := agent.New("ok", llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{{Content: "fine", FinishReason: "stop"}},
	}))
	srv, err := New(Options{Agents: []*agent.Agent{flaky, ok}, FailuresPath: feed})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts
}

func TestServerFailuresFeed(t *testing.T) {
	feed := filepath.Join(t.TempDir(), "failures.jsonl")
	ts := newFlakyServer(t, feed)

	resp, err := http.Post(ts.URL+"/api/chat", "application/json",
		strings.NewReader(`{"agent":"flaky","input":"add 1 and 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	recs, err := os.ReadFile(feed)
	if err != nil {
		t.Fatalf("feed not written: %v", err)
	}
	var rec eval.FailureRecord
	if err := json.Unmarshal(recs, &rec); err != nil {
		t.Fatalf("feed record invalid: %v (%q)", err, recs)
	}
	if rec.Input != "add 1 and 1" {
		t.Fatalf("input = %q", rec.Input)
	}
	if rec.Status != "failed" {
		t.Fatalf("status = %q", rec.Status)
	}
	if len(rec.ToolCalls) == 0 || rec.ToolCalls[0].Name != "calculator" {
		t.Fatalf("tool calls = %+v", rec.ToolCalls)
	}
	// A completed run must NOT land in the feed.
	before, _ := os.ReadFile(feed)
	resp2, err := http.Post(ts.URL+"/api/chat", "application/json",
		strings.NewReader(`{"agent":"ok","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	after, _ := os.ReadFile(feed)
	if string(before) != string(after) {
		t.Fatal("completed run polluted the failures feed")
	}
}

// ---------------------------------------------------------------------------
// trace ingestion (any framework can push traces)
// ---------------------------------------------------------------------------

func TestIngestTrace(t *testing.T) {
	ts := newTestServer(t)
	body := `{"traceId":"tr-123","name":"langchain-agent","agent":"external","status":"completed","spans":[{"id":"sp-1","runId":"tr-123","name":"llm","kind":"llm","status":"ok","startedAt":"2026-01-01T00:00:00Z","durationMs":10}],"metrics":{"iterations":2,"costCents":1.5}}`
	resp, err := http.Post(ts.URL+"/api/traces", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest status = %d", resp.StatusCode)
	}

	r2, err := http.Get(ts.URL + "/api/traces/tr-123")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", r2.StatusCode)
	}
	var trace struct {
		Source string           `json:"source"`
		Spans  []core.TraceSpan `json:"spans"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&trace); err != nil {
		t.Fatal(err)
	}
	if trace.Source != "ingested" || len(trace.Spans) != 1 {
		t.Fatalf("trace = %+v", trace)
	}
}

// ---------------------------------------------------------------------------
// HTTP runner (ernest replay against a live server)
// ---------------------------------------------------------------------------

func TestHTTPRunnerReplay(t *testing.T) {
	call := core.ToolCall{ID: "r1", Name: "calculator", Arguments: []byte(`{"expression":"6*7"}`)}
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "The answer is 42", FinishReason: "stop"},
			{Content: "The answer is 42", FinishReason: "stop"},
			{Content: `{"score":0.9,"reason":"correct"}`, FinishReason: "stop"},
		},
	})
	math := agent.New("math", p)
	math.Tools = []*core.Tool{core.Calculator}
	srv, err := New(Options{Agents: []*agent.Agent{math}})
	if err != nil {
		t.Fatal(err)
	}
	mathTs := httptest.NewServer(srv.Handler())
	defer mathTs.Close()
	defer srv.Close()

	runner := eval.HTTPRunner{Endpoint: mathTs.URL, Agent: "math", JudgeProvider: p}
	results, err := eval.RunAll(context.Background(), runner, []eval.Scenario{
		{
			Name:  "math",
			Input: "6*7?",
			Expect: eval.Expectation{
				OutputContains: []string{"42"},
				ToolCalls:      []eval.ToolExpectation{{Name: "calculator", ArgsContains: map[string]any{"expression": "6*7"}}},
			},
		},
		{
			Name:  "judged",
			Input: "6*7?",
			Judge: &eval.JudgeConfig{Rubric: "Must say 42."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	for _, r := range results {
		if !r.Pass {
			t.Fatalf("%s failed: %v", r.Name, r.Failures)
		}
	}
	// The judge ran over HTTP too (the script covered the fourth call:
	// scenario 2's agent turn then its judge turn).
	if results[1].JudgeScore != 0.9 {
		t.Fatalf("judge score = %.2f", results[1].JudgeScore)
	}
	// Model lookup against the live server.
	if m := runner.Model(context.Background()); m != "mock-1" {
		t.Fatalf("model = %q", m)
	}
}

func TestHTTPRunnerDown(t *testing.T) {
	runner := eval.HTTPRunner{Endpoint: "http://127.0.0.1:1", Agent: "x"} // nothing listens on :1
	if _, err := eval.RunAll(context.Background(), runner, []eval.Scenario{{Name: "x", Input: "hi", Expect: eval.Expectation{Status: "completed"}}}); err == nil {
		t.Fatal("expected connection error")
	}
}
