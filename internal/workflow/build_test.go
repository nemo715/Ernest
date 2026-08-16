package workflow

import (
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

func TestBuildValidation(t *testing.T) {
	agents := map[string]*agent.Agent{
		"a": agent.New("a", llm.NewMock(llm.MockConfig{})),
	}
	cases := []struct {
		name  string
		wf    string
		specs []StepSpec
		want  string
	}{
		{"no name", "", []StepSpec{{Name: "s", Agent: "a"}}, "name is required"},
		{"no steps", "w", nil, "no steps"},
		{"step without name", "w", []StepSpec{{Agent: "a"}}, "step without name"},
		{"duplicate step", "w", []StepSpec{{Name: "s", Agent: "a"}, {Name: "s", Agent: "a"}}, "duplicate step"},
		{"agent required", "w", []StepSpec{{Name: "s"}}, "agent is required"},
		{"unknown agent", "w", []StepSpec{{Name: "s", Agent: "ghost"}}, "unknown agent"},
		{"unknown dependency", "w", []StepSpec{{Name: "s", Agent: "a", DependsOn: []string{"ghost"}}}, "unknown step"},
		{"guard without rubric", "w", []StepSpec{{Name: "s", Agent: "a", Guard: &GuardSpec{}}}, "rubric"},
		{"cycle", "w", []StepSpec{
			{Name: "a", Agent: "a", DependsOn: []string{"b"}},
			{Name: "b", Agent: "a", DependsOn: []string{"a"}},
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.wf, tc.specs, agents, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildRunsDAGWithInterpolation(t *testing.T) {
	// Two scripted turns: the research step answer, then the write step
	// answer.
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{Content: "facts about go", FinishReason: "stop"},
		{Content: "summary paragraph", FinishReason: "stop"},
	}})
	agents := map[string]*agent.Agent{"worker": agent.New("worker", p)}
	wf, err := Build("pipeline", []StepSpec{
		{Name: "research", Agent: "worker", Prompt: "research {{input}}"},
		{Name: "write", Agent: "worker", Prompt: "write from {{research}}", DependsOn: []string{"research"}},
	}, agents, 0)
	if err != nil {
		t.Fatal(err)
	}

	res, err := wf.Run(t.Context(), "go concurrency")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if !strings.Contains(res.Output, `"research":"facts about go"`) {
		t.Fatalf("output missing research: %s", res.Output)
	}
	// The write step's prompt interpolated {{research}} and {{input}}.
	if len(p.Requests) != 2 {
		t.Fatalf("provider calls = %d", len(p.Requests))
	}
	var writePrompt string
	for _, m := range p.Requests[1].Messages {
		if m.Role == core.RoleUser {
			writePrompt = m.Content
		}
	}
	if writePrompt != "write from facts about go" {
		t.Fatalf("write prompt = %q", writePrompt)
	}
}

func TestBuildGuardPasses(t *testing.T) {
	// First call: the step output. Second call: the judge verdict.
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{Content: "a precise answer", FinishReason: "stop"},
		{Content: `{"score": 0.95, "reason": "on topic and correct"}`, FinishReason: "stop"},
	}})
	agents := map[string]*agent.Agent{"worker": agent.New("worker", p)}
	wf, err := Build("guarded", []StepSpec{
		{Name: "answer", Agent: "worker", Prompt: "answer {{input}}",
			Guard: &GuardSpec{Rubric: "The answer must address the task.", MinScore: 0.7}},
	}, agents, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := wf.Run(t.Context(), "a question")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
}

func TestBuildGuardFailsBelowMinScore(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{Content: "a sloppy answer", FinishReason: "stop"},
		{Content: `{"score": 0.2, "reason": "misses the point"}`, FinishReason: "stop"},
	}})
	agents := map[string]*agent.Agent{"worker": agent.New("worker", p)}
	wf, err := Build("guarded", []StepSpec{
		{Name: "answer", Agent: "worker", Prompt: "answer {{input}}",
			Guard: &GuardSpec{Rubric: "The answer must address the task.", MinScore: 0.7}},
	}, agents, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := wf.Run(t.Context(), "a question")
	if err == nil {
		t.Fatal("workflow must fail when the guard score is below min")
	}
	if !strings.Contains(err.Error(), "guard score 0.20 < min 0.70") {
		t.Fatalf("err = %v", err)
	}
	if res.Status != core.RunStatusFailed {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestBuildGuardDefaultMinScore(t *testing.T) {
	// Guard with no explicit MinScore uses the 0.7 default.
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{Content: "ok", FinishReason: "stop"},
		{Content: `{"score": 0.6, "reason": "just under"}`, FinishReason: "stop"},
	}})
	agents := map[string]*agent.Agent{"worker": agent.New("worker", p)}
	wf, err := Build("guarded", []StepSpec{
		{Name: "answer", Agent: "worker", Prompt: "x", Guard: &GuardSpec{Rubric: "be good"}},
	}, agents, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wf.Run(t.Context(), "q")
	if err == nil || !strings.Contains(err.Error(), "min 0.70") {
		t.Fatalf("err = %v, want default min 0.70 failure", err)
	}
}
