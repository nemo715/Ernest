package eval

import (
	"context"
	"encoding/json"
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
