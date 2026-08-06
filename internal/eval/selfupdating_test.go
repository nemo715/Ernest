package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

// ---------------------------------------------------------------------------
// shape checks (tool-result payloads)
// ---------------------------------------------------------------------------

func TestShapePass(t *testing.T) {
	spec := &ShapeSpec{
		RequiredFields: []string{"items", "count"},
		FieldTypes:     map[string]string{"count": "int", "items": "array"},
	}
	if fails := checkShape(json.RawMessage(`{"items":[1,2],"count":2}`), spec); len(fails) != 0 {
		t.Fatalf("unexpected failures: %v", fails)
	}
}

func TestShapeMissingRequired(t *testing.T) {
	spec := &ShapeSpec{RequiredFields: []string{"items", "count"}}
	fails := checkShape(json.RawMessage(`{"items":[1]}`), spec)
	if len(fails) != 1 || !strings.Contains(fails[0], "missing required field \"count\"") {
		t.Fatalf("failures = %v", fails)
	}
}

func TestShapeEmptyArrayCaught(t *testing.T) {
	// The silent 200 OK: an empty array looks like success.
	fails := checkShape(json.RawMessage(`[]`), &ShapeSpec{MinItems: 1})
	if len(fails) != 1 || !strings.Contains(fails[0], "array has 0 items") {
		t.Fatalf("failures = %v", fails)
	}
}

func TestShapeEmptyStringCaught(t *testing.T) {
	fails := checkShape(json.RawMessage(`""`), &ShapeSpec{MinLength: 1})
	if len(fails) != 1 || !strings.Contains(fails[0], "length 0") {
		t.Fatalf("failures = %v", fails)
	}
}

func TestShapeWrongType(t *testing.T) {
	fails := checkShape(json.RawMessage(`{"count":3.5}`), &ShapeSpec{FieldTypes: map[string]string{"count": "int"}})
	if len(fails) != 1 || !strings.Contains(fails[0], `field "count": expected int, got number`) {
		t.Fatalf("failures = %v", fails)
	}
}

func TestShapeNotObject(t *testing.T) {
	fails := checkShape(json.RawMessage(`[1,2,3]`), &ShapeSpec{RequiredFields: []string{"id"}})
	if len(fails) != 1 || !strings.Contains(fails[0], "expected object") {
		t.Fatalf("failures = %v", fails)
	}
}

func TestShapeDoubleEncodedUnwraps(t *testing.T) {
	// Tools that return a JSON string containing JSON should be
	// validated against the inner structure.
	content, _ := json.Marshal(`{"id":7,"ok":true}`)
	fails := checkShape(content, &ShapeSpec{FieldTypes: map[string]string{"id": "int", "ok": "bool"}})
	if len(fails) != 0 {
		t.Fatalf("unexpected failures: %v", fails)
	}
}

func TestShapeGarbage(t *testing.T) {
	fails := checkShape(json.RawMessage(`not json`), &ShapeSpec{MinItems: 1})
	if len(fails) != 1 {
		t.Fatalf("failures = %v", fails)
	}
}

// emptyResultTool returns an empty array — the silent 200 OK the shape
// checks exist to catch.
func emptyResultTool() *core.Tool {
	return core.MustTool[struct {
		Q string `json:"q"`
	}]("search", "Search the catalog", func(ctx context.Context, tc *core.ToolContext, args struct {
		Q string `json:"q"`
	}) (any, error) {
		return []any{}, nil
	})
}

func TestEvalShapeCatchEmptyArray(t *testing.T) {
	call := core.ToolCall{ID: "s1", Name: "search", Arguments: []byte(`{"q":"x"}`)}
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "No results found", FinishReason: "stop"},
		},
	})
	a := agent.New("catalog", p)
	a.Tools = []*core.Tool{emptyResultTool()}
	res, err := Run(context.Background(), AgentRunner{Agent: a}, Scenario{
		Name:  "search",
		Input: "find x",
		Expect: Expectation{
			ToolResults: []ToolResultExpectation{{Name: "search", Shape: &ShapeSpec{MinItems: 1}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("expected failure: empty array must not pass minItems 1")
	}
	if len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "array has 0 items") {
		t.Fatalf("failures = %v", res.Failures)
	}
}

func TestEvalToolErrorExpectation(t *testing.T) {
	call := core.ToolCall{ID: "s1", Name: "fetch", Arguments: []byte(`{"url":"http://x"}`)}
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "fetch failed", FinishReason: "stop"},
		},
	})
	a := agent.New("net", p)
	a.Tools = []*core.Tool{core.MustTool[struct {
		URL string `json:"url"`
	}]("fetch", "Fetch a URL", func(ctx context.Context, tc *core.ToolContext, args struct {
		URL string `json:"url"`
	}) (any, error) {
		return nil, fmt.Errorf("connection refused to %s", args.URL)
	})}

	// Expecting the failure: passes.
	res, err := Run(context.Background(), AgentRunner{Agent: a}, Scenario{
		Name:  "fetch",
		Input: "fetch http://x",
		Expect: Expectation{
			ToolResults: []ToolResultExpectation{{Name: "fetch", ErrorContains: "connection refused"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected pass: %+v", res)
	}

	// Expecting success (shape): fails because the tool errored. A
	// fresh agent: the mock script is consumed per run.
	a2 := agent.New("net", llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
			{Content: "fetch failed", FinishReason: "stop"},
		},
	}))
	a2.Tools = a.Tools
	res2, err := Run(context.Background(), AgentRunner{Agent: a2}, Scenario{
		Name:  "fetch-shape",
		Input: "fetch http://x",
		Expect: Expectation{
			ToolResults: []ToolResultExpectation{{Name: "fetch", Shape: &ShapeSpec{MinLength: 1}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pass || len(res2.Failures) != 1 || !strings.Contains(res2.Failures[0], "failed: connection refused") {
		t.Fatalf("res2 = %+v", res2)
	}
}

func TestEvalNoMatchingToolResult(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "plain answer", FinishReason: "stop"}}})
	a := agent.New("plain", p)
	res, err := Run(context.Background(), AgentRunner{Agent: a}, Scenario{
		Name:  "plain",
		Input: "hi",
		Expect: Expectation{
			ToolResults: []ToolResultExpectation{{Name: "search", Shape: &ShapeSpec{MinItems: 1}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "no tool result matching search") {
		t.Fatalf("res = %+v", res)
	}
}

// ---------------------------------------------------------------------------
// learning (failures -> scenarios)
// ---------------------------------------------------------------------------

func rec(input, errMsg string, results []core.ToolResult) FailureRecord {
	return FailureRecord{
		RunID:       "r1",
		Agent:       "assistant",
		Input:       input,
		Output:      "partial",
		Status:      "failed",
		Error:       errMsg,
		ToolCalls:   []core.ToolCall{{ID: "c1", Name: "search", Arguments: []byte(`{"q":"x"}`)}},
		ToolResults: results,
		At:          time.Now(),
	}
}

func TestLearnDeduplicates(t *testing.T) {
	records := []FailureRecord{
		rec("find the widget", "max iterations reached", nil),
		rec("find the widget", "max iterations reached", nil), // identical: same fingerprint
	}
	learned := LearnFailure(records, nil, 0)
	if len(learned) != 1 {
		t.Fatalf("learned = %d, want 1 (dedupe)", len(learned))
	}
}

func TestLearnSkipsCoveredInputs(t *testing.T) {
	existing := []Scenario{{Name: "covered", Input: "find the widget", Expect: Expectation{Status: "completed"}}}
	records := []FailureRecord{rec("find the widget", "boom", nil)}
	if learned := LearnFailure(records, existing, 0); len(learned) != 0 {
		t.Fatalf("learned = %d, want 0 (already covered)", len(learned))
	}
}

func TestLearnFailedRunScenario(t *testing.T) {
	learned := LearnFailure([]FailureRecord{rec("find the widget", "max iterations reached", nil)}, nil, 0)
	if len(learned) != 1 {
		t.Fatal("expected one learned scenario")
	}
	sc := learned[0].Scenario
	if sc.Input != "find the widget" {
		t.Fatalf("input = %q", sc.Input)
	}
	if sc.Expect.Status != "completed" {
		t.Fatalf("status expectation = %q, want completed", sc.Expect.Status)
	}
	if !strings.Contains(sc.Name, "find-the-widget") {
		t.Fatalf("name = %q, want slug prefix", sc.Name)
	}
}

func TestLearnEmptyArrayBecomesShapeCheck(t *testing.T) {
	tr := core.ToolResult{ID: "t1", Name: "search", Content: json.RawMessage(`[]`)}
	learned := LearnFailure([]FailureRecord{rec("find the widget", "", []core.ToolResult{tr})}, nil, 0)
	if len(learned) != 1 {
		t.Fatal("expected one learned scenario")
	}
	exp := learned[0].Scenario.Expect.ToolResults
	if len(exp) != 1 || exp[0].Name != "search" || exp[0].Shape == nil || exp[0].Shape.MinItems != 1 {
		t.Fatalf("toolResults expectation = %+v", exp)
	}
}

func TestLearnToolErrorBecomesErrorContains(t *testing.T) {
	tr := core.ToolResult{ID: "t1", Name: "fetch", Error: "connection refused"}
	learned := LearnFailure([]FailureRecord{rec("fetch x", "", []core.ToolResult{tr})}, nil, 0)
	exp := learned[0].Scenario.Expect.ToolResults
	if len(exp) != 1 || exp[0].Name != "fetch" || exp[0].ErrorContains != "connection refused" {
		t.Fatalf("toolResults expectation = %+v", exp)
	}
}

func TestLearnMaxCaps(t *testing.T) {
	var records []FailureRecord
	for i := 0; i < 10; i++ {
		records = append(records, rec(fmt.Sprintf("input number %d", i), "boom", nil))
	}
	if learned := LearnFailure(records, nil, 3); len(learned) != 3 {
		t.Fatalf("learned = %d, want 3", len(learned))
	}
}

func TestLoadFailures(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/failures.jsonl"
	lines := []string{
		`{"input":"a","status":"failed","error":"x"}`,
		`{"input":"b","status":"failed","error":"y"}`,
	}
	if err := writeFileLines(path, lines); err != nil {
		t.Fatal(err)
	}
	recs, err := LoadFailures(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Input != "a" {
		t.Fatalf("recs = %+v", recs)
	}
}

func TestLoadFailuresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := writeFileLines(path, []string{`{"input":"a"}`, `not json`}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFailures(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}

func writeFileLines(path string, lines []string) error {
	data := []byte(strings.Join(lines, "\n") + "\n")
	return os.WriteFile(path, data, 0o644)
}

// ---------------------------------------------------------------------------
// rubric generation
// ---------------------------------------------------------------------------

func TestGenerateRubric(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{{Content: "The answer must state 391.", FinishReason: "stop"}},
	})
	rubric, err := GenerateRubric(context.Background(), p, rec("what is 17*23", "wrong answer", nil))
	if err != nil {
		t.Fatal(err)
	}
	if rubric != "The answer must state 391." {
		t.Fatalf("rubric = %q", rubric)
	}
}
