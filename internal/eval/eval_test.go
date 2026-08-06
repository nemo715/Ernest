package eval

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

func testAgent(t *testing.T) *agent.Agent {
	t.Helper()
	call := core.ToolCall{ID: "e1", Name: "calculator", Arguments: []byte(`{"expression":"6*7"}`)}
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "The answer is 42", FinishReason: "stop"},
		},
	})
	a := agent.New("math", p)
	a.Tools = []*core.Tool{core.Calculator}
	return a
}

func TestEvalPass(t *testing.T) {
	a := testAgent(t)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "math",
		Input: "6*7?",
		Expect: Expectation{
			OutputContains: []string{"42"},
			ToolCalls:      []ToolExpectation{{Name: "calculator", ArgsContains: map[string]any{"expression": "6*7"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass: %+v", res)
	}
}

func TestEvalFailOutput(t *testing.T) {
	a := testAgent(t)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "wrong",
		Input: "6*7?",
		Expect: Expectation{
			OutputContains: []string{"no such answer"},
			ToolCalls:      []ToolExpectation{{Name: "calculator", ArgsContains: map[string]any{"expression": "1/0"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("expected failure")
	}
	if len(res.Failures) != 2 {
		t.Fatalf("failures = %v", res.Failures)
	}
}

func TestEvalNoToolCalls(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "hi there", FinishReason: "stop"}}})
	a := agent.New("plain", p)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "plain",
		Input: "hello",
		Expect: Expectation{
			NoToolCalls:    true,
			OutputContains: []string{"hi there"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass: %+v", res)
	}
}

func TestEvalAwaitingApprovalStatus(t *testing.T) {
	call := core.ToolCall{ID: "a1", Name: "send_email", Arguments: []byte(`{"to":"x@y.z"}`)}
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"}},
	})
	a := agent.New("gated", p)
	a.Tools = []*core.Tool{core.MustTool[struct {
		To string `json:"to"`
	}]("send_email", "Send an email", func(ctx context.Context, tc *core.ToolContext, args struct {
		To string `json:"to"`
	}) (any, error) {
		return nil, tc.RequestApproval("send_email", "send to "+args.To, nil)
	})}
	res, err := Run(context.Background(), a, Scenario{
		Name:  "gated",
		Input: "send",
		Expect: Expectation{
			Status: "awaiting_approval",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass: %+v", res)
	}
}

func TestLoadScenarios(t *testing.T) {
	dir := t.TempDir()
	multi := filepath.Join(dir, "multi.json")
	one := filepath.Join(dir, "one.json")
	data := map[string]any{
		"scenarios": []map[string]any{
			{"name": "a", "input": "hi", "expect": map[string]any{"outputContains": []string{"x"}}},
			{"name": "b", "input": "yo", "expect": map[string]any{"status": "completed"}},
		},
	}
	b, _ := json.Marshal(data)
	if err := os.WriteFile(multi, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(one, []byte(`{"name":"c","input":"go","expect":{"noToolCalls":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	scs, err := LoadScenarios(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scs) != 3 {
		t.Fatalf("scenarios = %d", len(scs))
	}
	names := []string{scs[0].Name, scs[1].Name, scs[2].Name}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("names = %v", names)
	}
}

func TestLoadScenariosMissingName(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(f, []byte(`{"name":"","input":"x","expect":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadScenarios(f); err == nil {
		t.Fatal("expected validation error")
	}
}

// ---------------------------------------------------------------------------
// LLM-as-judge
// ---------------------------------------------------------------------------

// judgedAgent builds an agent whose mock script ends with a judge turn
// (the judge reuses the agent's provider, so scripts must cover it).
func judgedAgent(t *testing.T, judgeJSON string) *agent.Agent {
	t.Helper()
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{Content: "The square root of 144 is 12", FinishReason: "stop"},
			{Content: judgeJSON, FinishReason: "stop"},
		},
	})
	a := agent.New("math", p)
	return a
}

func TestEvalJudgePass(t *testing.T) {
	a := judgedAgent(t, `{"score":0.9,"reason":"correct and concise"}`)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "sqrt",
		Input: "sqrt(144)?",
		Judge: &JudgeConfig{Rubric: "Must give the correct square root."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass: %+v", res)
	}
	if res.JudgeScore != 0.9 || res.JudgeVerdict != "pass" {
		t.Fatalf("judge = %.2f [%s]", res.JudgeScore, res.JudgeVerdict)
	}
}

func TestEvalJudgeBelowMin(t *testing.T) {
	a := judgedAgent(t, `{"score":0.4,"reason":"missing units"}`)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "sqrt",
		Input: "sqrt(144)?",
		Judge: &JudgeConfig{Rubric: "Must include units."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatalf("expected fail below default min 0.7: %+v", res)
	}
	if res.JudgeVerdict != "fail" {
		t.Fatalf("verdict = %s", res.JudgeVerdict)
	}
	if len(res.Failures) == 0 || !strings.Contains(res.Failures[0], "judge score 0.40 < min 0.70") {
		t.Fatalf("failures = %v", res.Failures)
	}
}

func TestEvalJudgeCustomMin(t *testing.T) {
	a := judgedAgent(t, `{"score":0.4,"reason":"close enough"}`)
	res, err := Run(context.Background(), a, Scenario{
		Name:  "sqrt",
		Input: "sqrt(144)?",
		Judge: &JudgeConfig{Rubric: "Approximate answers are fine.", MinScore: 0.3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass with custom min: %+v", res)
	}
}

func TestEvalJudgeInvalidJSON(t *testing.T) {
	a := judgedAgent(t, "sure, that looks right")
	res, err := Run(context.Background(), a, Scenario{
		Name:  "sqrt",
		Input: "sqrt(144)?",
		Judge: &JudgeConfig{Rubric: "Be strict."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("expected fail on invalid judge verdict")
	}
	if len(res.Failures) == 0 || !strings.Contains(res.Failures[0], "judge returned no JSON") {
		t.Fatalf("failures = %v", res.Failures)
	}
}

// ---------------------------------------------------------------------------
// summary, baselines and regression
// ---------------------------------------------------------------------------

func mkResult(name string, pass bool, score float64) *Result {
	return &Result{Name: name, Pass: pass, JudgeScore: score, CostCents: 1.25, DurationMS: 10}
}

func TestSummarize(t *testing.T) {
	s := Summarize("math", []*Result{
		mkResult("a", true, 0.9),
		mkResult("b", false, 0.3),
	})
	if s.Scenarios != 2 || s.Passed != 1 || s.Failed != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.TotalCostCents != 2.5 || s.TotalDurationMS != 20 {
		t.Fatalf("totals = %.2f / %d", s.TotalCostCents, s.TotalDurationMS)
	}
}

func TestBaselineRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	results := []*Result{mkResult("a", true, 0.9), mkResult("b", true, 0.85)}
	if err := SaveBaseline(path, results); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a" || !got[0].Pass || got[0].JudgeScore != 0.9 {
		t.Fatalf("roundtrip = %+v", got)
	}
}

func TestRegressClean(t *testing.T) {
	base := []*Result{mkResult("a", true, 0.9), mkResult("b", true, 0.8)}
	cur := []*Result{mkResult("a", true, 0.9), mkResult("b", true, 0.82)}
	regs := Regress(cur, base)
	if len(regs) != 0 {
		t.Fatalf("expected no regressions: %+v", regs)
	}
}

func TestRegressPassToFail(t *testing.T) {
	base := []*Result{mkResult("a", true, 0.9)}
	cur := []*Result{mkResult("a", false, 0.3)}
	regs := Regress(cur, base)
	if len(regs) != 1 {
		t.Fatalf("regs = %+v", regs)
	}
	r := regs[0]
	if r.WasPass != true || r.NowPass != false {
		t.Fatalf("reg = %+v", r)
	}
	if d := math.Abs(r.ScoreDelta - (-0.6)); d > 1e-9 {
		t.Fatalf("score delta = %v", r.ScoreDelta)
	}
	if CountRegressions(regs) != 1 {
		t.Fatal("expected 1 regression")
	}
}

func TestRegressScoreDropWarns(t *testing.T) {
	base := []*Result{mkResult("a", true, 0.95)}
	cur := []*Result{mkResult("a", true, 0.6)} // still passing, big score dip
	regs := Regress(cur, base)
	if len(regs) != 1 || !strings.Contains(regs[0].Note, "judge score dropped") {
		t.Fatalf("regs = %+v", regs)
	}
	// Score dips do not fail the gate.
	if CountRegressions(regs) != 0 {
		t.Fatal("score dip must not count as regression")
	}
}

func TestRegressMissingScenario(t *testing.T) {
	base := []*Result{mkResult("a", true, 0.9), mkResult("ghost", true, 0.9)}
	cur := []*Result{mkResult("a", true, 0.9)}
	regs := Regress(cur, base)
	if len(regs) != 1 || regs[0].Name != "ghost" {
		t.Fatalf("regs = %+v", regs)
	}
	if CountRegressions(regs) != 1 {
		t.Fatal("removed scenario must count as regression")
	}
}

func TestRegressNewScenario(t *testing.T) {
	base := []*Result{mkResult("a", true, 0.9)}
	cur := []*Result{mkResult("a", true, 0.9), mkResult("fresh", false, 0.2)}
	regs := Regress(cur, base)
	if len(regs) != 1 || !regs[0].New || regs[0].Name != "fresh" {
		t.Fatalf("regs = %+v", regs)
	}
}
