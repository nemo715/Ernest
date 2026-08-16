// Package live is the real-model integration suite: every test here
// talks to a live OpenAI-compatible endpoint (OpenRouter) using the
// OPENROUTER_API_KEY environment variable. The suite is skipped when
// the key is missing or -short is set, so CI without the secret and
// quick local runs stay deterministic and free.
//
// Run: set OPENROUTER_API_KEY, then `go test ./internal/live -count=1 -v`
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	serverInternal "github.com/nemo715/Ernest/internal/server"
	workflowInternal "github.com/nemo715/Ernest/internal/workflow"
	"github.com/nemo715/Ernest/llm"
	serverPublic "github.com/nemo715/Ernest/server"
	"github.com/nemo715/Ernest/team"
)

// liveModel is the model every live test uses (cheap, fast, reliable).
const liveModel = "openai/gpt-4o-mini"

func liveProvider() llm.Provider {
	return llm.NewOpenAICompat(llm.OpenAICompatConfig{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Model:   liveModel,
	})
}

func requireLive(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("live suite: skipped in -short mode")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("live suite: set OPENROUTER_API_KEY to run real-model tests")
	}
}

func liveContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 240*time.Second)
}

// TestLiveAgentChat runs one turn against the real model through the
// public agent API and asserts the run completed with real usage.
func TestLiveAgentChat(t *testing.T) {
	requireLive(t)
	ctx, cancel := liveContext(t)
	defer cancel()

	a := agent.New("assistant", liveProvider())
	a.Instructions = "You are a concise assistant. Answer in one sentence."
	res, err := a.Chat(ctx, "What is the capital of France?", agent.RunOptions{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Fatal("empty output from the live model")
	}
	if !strings.Contains(strings.ToLower(res.Output), "paris") {
		t.Fatalf("output missing expected answer: %q", res.Output)
	}
	if res.Usage == nil || res.Usage.OutputTokens == 0 {
		t.Fatal("usage not reported for the live run")
	}
}

// TestLiveToolLoop asks the live model an arithmetic question with the
// calculator tool attached; the model must actually call it.
func TestLiveToolLoop(t *testing.T) {
	requireLive(t)
	ctx, cancel := liveContext(t)
	defer cancel()

	a := agent.New("math", liveProvider())
	a.Instructions = "Use the calculator tool for arithmetic. Answer with the number only."
	a.Tools = []*core.Tool{core.ToolsByName(core.BuiltinTools)["calculator"]}

	res, err := a.Chat(ctx, "what is 6*7?", agent.RunOptions{MaxIterations: 4})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if !strings.Contains(res.Output, "42") {
		t.Fatalf("output = %q, want 42", res.Output)
	}
	// A tool roundtrip means >2 messages (user, assistant tool_call, tool,
	// assistant final); the model must have used the calculator.
	if len(res.Messages) < 3 {
		t.Fatalf("messages = %d, want a tool roundtrip; the model skipped the calculator", len(res.Messages))
	}
}

// TestLiveSequentialTeam runs a two-member sequential team over the real
// model: researcher then writer, output threaded member-to-member.
func TestLiveSequentialTeam(t *testing.T) {
	requireLive(t)
	ctx, cancel := liveContext(t)
	defer cancel()

	researcher := agent.New("researcher", liveProvider())
	researcher.Instructions = "You research facts. State findings plainly, numbers included."
	writer := agent.New("writer", liveProvider())
	writer.Instructions = "You condense findings into one short sentence, keeping any numbers."

	tm := team.New("editorial", researcher, writer)
	tm.Process = "sequential"
	res, err := tm.Chat(ctx, "what is 6*7?", agent.RunOptions{})
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if !strings.Contains(res.Output, "42") {
		t.Fatalf("team output = %q, want 42", res.Output)
	}
}

// TestLiveWorkflowWithGuard runs a declarative workflow whose output is
// judged by the real model against a rubric (the LLM-judged guard).
func TestLiveWorkflowWithGuard(t *testing.T) {
	requireLive(t)
	ctx, cancel := liveContext(t)
	defer cancel()

	worker := agent.New("worker", liveProvider())
	worker.Instructions = "Answer briefly and accurately."
	wf, err := workflowInternal.Build("guarded", []workflowInternal.StepSpec{
		{
			Name:   "answer",
			Agent:  "worker",
			Prompt: "Answer {{input}}",
			Guard: &workflowInternal.GuardSpec{
				Rubric:   "The answer must address the question directly.",
				MinScore: 0.5,
			},
		},
	}, map[string]*agent.Agent{"worker": worker}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := wf.Run(ctx, "What is the capital of France?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if !strings.Contains(strings.ToLower(res.Output), "paris") {
		t.Fatalf("output = %q", res.Output)
	}
}

// noteArgs is the argument shape of the HITL demo tool.
type noteArgs struct {
	Note string `json:"note" jsonschema:"The note text to record"`
}

// TestLiveServerChatAndHITL drives the public HTTP surface against the
// live model: an SSE chat, then a full human-in-the-loop roundtrip
// (model asks for approval -> human approves -> run resumes).
func TestLiveServerChatAndHITL(t *testing.T) {
	requireLive(t)

	assistant := agent.New("assistant", liveProvider())
	assistant.Instructions = "You are a helpful assistant. Be brief."

	hitl := agent.New("hitl", liveProvider())
	hitl.Instructions = "You record notes. ALWAYS call send_note with the note text before replying."
	hitl.Tools = []*core.Tool{core.MustTool[noteArgs]("send_note", "Record a note (requires human approval)", func(ctx context.Context, tc *core.ToolContext, args noteArgs) (any, error) {
		if err := tc.RequestApproval("send_note", "Record note: "+args.Note, map[string]any{"note": args.Note}); err != nil {
			return nil, err
		}
		return map[string]any{"recorded": true}, nil
	})}

	srv, err := serverPublic.New(serverPublic.Options{Agents: []*agent.Agent{assistant, hitl}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Close() })

	// 1. plain chat over SSE against the live model.
	events := postSSE(t, ts.URL+"/api/chat", `{"agent":"assistant","input":"Say hello in one word"}`)
	complete := lastComplete(t, events)
	if complete.Status != core.RunStatusCompleted {
		t.Fatalf("chat status = %s (%s)", complete.Status, complete.Error)
	}
	if strings.TrimSpace(complete.Output) == "" {
		t.Fatal("empty live output over SSE")
	}

	// 2. HITL roundtrip: the model asks for approval, a human approves,
	//    the run resumes and completes.
	events = postSSE(t, ts.URL+"/api/chat", `{"agent":"hitl","input":"write a note about the live test"}`)
	paused := lastComplete(t, events)
	if paused.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("HITL pause status = %s, want awaiting_approval", paused.Status)
	}
	approvalID := ""
	for _, ev := range events {
		if ev.Approval != nil {
			approvalID = ev.Approval.ID
		}
	}
	if approvalID == "" {
		t.Fatal("no approval.requested frame in the HITL run")
	}
	events = postSSE(t, ts.URL+"/api/approve",
		fmt.Sprintf(`{"agent":"hitl","approvalId":%q,"approved":true,"note":"ok"}`, approvalID))
	resumed := lastComplete(t, events)
	if resumed.Status != core.RunStatusCompleted {
		t.Fatalf("resumed status = %s (%s)", resumed.Status, resumed.Error)
	}
}

// TestLiveServerTeamWorkflowSSE runs config-declared teams and workflows
// over the server routes with real models on every member/step.
func TestLiveServerTeamWorkflowSSE(t *testing.T) {
	requireLive(t)

	lead := agent.New("lead", liveProvider())
	lead.Instructions = "You coordinate the team."
	researcher := agent.New("researcher", liveProvider())
	researcher.Instructions = "You research topics and state facts."
	writer := agent.New("writer", liveProvider())
	writer.Instructions = "You condense findings into one sentence, keeping any numbers."

	srv, err := serverInternal.New(serverInternal.Options{
		Agents: []*agent.Agent{lead, researcher, writer},
		Teams: []serverInternal.TeamSpec{
			{
				Name:    "editorial",
				Leader:  "lead",
				Members: []string{"researcher", "writer"},
				Process: "sequential",
			},
		},
		Workflows: []serverInternal.WorkflowSpec{
			{
				Name: "pipeline",
				Steps: []serverInternal.WorkflowStepSpec{
					{Name: "research", Agent: "researcher", Prompt: "Find the answer to {{input}} and state it."},
					{Name: "write", Agent: "writer", Prompt: "Condense: {{research}}", DependsOn: []string{"research"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Close() })

	events := postSSE(t, ts.URL+"/api/teams/editorial/run", `{"input":"what is 6*7?"}`)
	complete := lastComplete(t, events)
	if complete.Status != core.RunStatusCompleted {
		t.Fatalf("team status = %s (%s)", complete.Status, complete.Error)
	}
	if !strings.Contains(complete.Output, "42") {
		t.Fatalf("team output = %q, want 42", complete.Output)
	}

	events = postSSE(t, ts.URL+"/api/workflows/pipeline/run", `{"input":"what is 6*7?"}`)
	complete = lastComplete(t, events)
	if complete.Status != core.RunStatusCompleted {
		t.Fatalf("workflow status = %s (%s)", complete.Status, complete.Error)
	}
	if !strings.Contains(complete.Output, "42") {
		t.Fatalf("workflow output = %q, want 42", complete.Output)
	}
}

// postSSE POSTs body to url and decodes every SSE frame into events.
func postSSE(t *testing.T, url, body string) []core.RunEvent {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, data)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var events []core.RunEvent
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev core.RunEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}

// lastComplete returns the final run.complete result in the stream.
func lastComplete(t *testing.T, events []core.RunEvent) *core.RunResult {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == core.EventRunComplete && events[i].Result != nil {
			return events[i].Result
		}
	}
	t.Fatal("stream ended without run.complete")
	return nil
}
