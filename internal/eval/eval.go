// Package eval runs deterministic scenario checks against agents: the
// "ernest eval" harness. Scenarios are declarative checks on agent
// behaviour (output text, tool calls, run status) that can be run
// against any provider — the mock provider makes them fully
// deterministic in CI.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
)

// ToolExpectation checks one tool call made by the agent.
type ToolExpectation struct {
	Name         string         `json:"name"`
	ArgsContains map[string]any `json:"argsContains,omitempty"`
}

// Expectation is the pass/fail contract of a scenario.
type Expectation struct {
	OutputContains []string          `json:"outputContains,omitempty"`
	ToolCalls      []ToolExpectation `json:"toolCalls,omitempty"`
	NoToolCalls    bool              `json:"noToolCalls,omitempty"`
	Status         string            `json:"status,omitempty"` // completed | awaiting_approval | failed
}

// Scenario is one evaluable behaviour check.
type Scenario struct {
	Name   string      `json:"name"`
	Input  string      `json:"input"`
	Expect Expectation `json:"expect"`
}

// Result is the outcome of one scenario.
type Result struct {
	Name       string   `json:"name"`
	Pass       bool     `json:"pass"`
	Failures   []string `json:"failures,omitempty"`
	Output     string   `json:"output,omitempty"`
	Status     string   `json:"status,omitempty"`
	DurationMS int64    `json:"durationMs"`
}

// Run executes one scenario against the agent and reports pass/fail.
// Runs are ephemeral (no session persistence) unless opts override it.
func Run(ctx context.Context, a *agent.Agent, sc Scenario, opts ...agent.RunOptions) (*Result, error) {
	start := time.Now()
	opt := agent.RunOptions{SkipMemory: true}
	if len(opts) > 0 {
		opt = opts[0]
	}
	ch, err := a.Stream(ctx, sc.Input, opt)
	if err != nil {
		return nil, err
	}
	var calls []core.ToolCall
	var res *core.RunResult
	for ev := range ch {
		if ev.ToolCall != nil {
			calls = append(calls, *ev.ToolCall)
		}
		if ev.Result != nil {
			res = ev.Result
		}
	}
	if res == nil {
		return nil, errors.New("run produced no result")
	}
	r := &Result{
		Name:       sc.Name,
		Output:     res.Output,
		Status:     string(res.Status),
		DurationMS: time.Since(start).Milliseconds(),
	}
	fail := func(format string, args ...any) {
		r.Failures = append(r.Failures, fmt.Sprintf(format, args...))
	}

	if sc.Expect.Status != "" && r.Status != sc.Expect.Status {
		fail("status = %s, want %s", r.Status, sc.Expect.Status)
	}
	for _, want := range sc.Expect.OutputContains {
		if !strings.Contains(r.Output, want) {
			fail("output %q does not contain %q", r.Output, want)
		}
	}
	if sc.Expect.NoToolCalls && len(calls) > 0 {
		fail("expected no tool calls, got %d (%s)", len(calls), calls[0].Name)
	}
	for _, want := range sc.Expect.ToolCalls {
		matched := false
		for _, c := range calls {
			if c.Name != want.Name {
				continue
			}
			if len(want.ArgsContains) == 0 {
				matched = true
				break
			}
			var args map[string]any
			if err := json.Unmarshal(c.Arguments, &args); err == nil {
				if containsAll(args, want.ArgsContains) {
					matched = true
					break
				}
			}
		}
		if !matched {
			fail("no tool call matching %s %v", want.Name, want.ArgsContains)
		}
	}

	r.Pass = len(r.Failures) == 0
	return r, nil
}

// RunAll executes scenarios sequentially against the agent.
func RunAll(ctx context.Context, a *agent.Agent, scenarios []Scenario) ([]*Result, error) {
	out := make([]*Result, 0, len(scenarios))
	for _, sc := range scenarios {
		res, err := Run(ctx, a, sc)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: %w", sc.Name, err)
		}
		out = append(out, res)
	}
	return out, nil
}

// LoadScenarios reads scenario definitions from a file or a directory of
// files. Each file is either {"scenarios": [...]} or a single scenario.
func LoadScenarios(path string) ([]Scenario, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		var out []Scenario
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			// Never treat the project config (ernest.json) as scenarios when
			// the scenarios dir happens to contain it.
			if strings.EqualFold(e.Name(), "ernest.json") {
				continue
			}
			s, err := loadFile(filepath.Join(path, e.Name()))
			if err != nil {
				return nil, err
			}
			out = append(out, s...)
		}
		return out, nil
	}
	return loadFile(path)
}

func loadFile(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &doc); err == nil && len(doc.Scenarios) > 0 {
		return validateScenarios(doc.Scenarios)
	}
	var single Scenario
	if err := json.Unmarshal(data, &single); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return validateScenarios([]Scenario{single})
}

func validateScenarios(scs []Scenario) ([]Scenario, error) {
	for i, sc := range scs {
		if sc.Name == "" {
			return nil, fmt.Errorf("scenario %d: name is required", i)
		}
		if sc.Input == "" {
			return nil, fmt.Errorf("scenario %q: input is required", sc.Name)
		}
	}
	return scs, nil
}

func containsAll(args, want map[string]any) bool {
	for k, v := range want {
		got, ok := args[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", v) {
			return false
		}
	}
	return true
}
