// Package eval runs deterministic scenario checks against agents: the
// "ernest eval" harness. Scenarios are declarative checks on agent
// behaviour (output text, tool calls, run status) that can be run
// against any provider — the mock provider makes them fully
// deterministic in CI. Scenarios can also attach an LLM-as-judge that
// scores the output against a rubric with a second model call.
//
// Evals produce a Summary that can be persisted as a baseline and
// diffed later (ernest eval --baseline): scenarios that regress from
// pass to fail make the CLI exit non-zero, so evals gate deploys.
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
	"github.com/nemo715/Ernest/internal/llm"
)

// ToolExpectation checks one tool call made by the agent.
type ToolExpectation struct {
	Name         string         `json:"name"`
	ArgsContains map[string]any `json:"argsContains,omitempty"`
}

// Expectation is the pass/fail contract of a scenario.
type Expectation struct {
	OutputContains []string                `json:"outputContains,omitempty"`
	ToolCalls      []ToolExpectation       `json:"toolCalls,omitempty"`
	ToolResults    []ToolResultExpectation `json:"toolResults,omitempty"`
	NoToolCalls    bool                    `json:"noToolCalls,omitempty"`
	Status         string                  `json:"status,omitempty"` // completed | awaiting_approval | failed
}

// JudgeConfig attaches an LLM-as-judge check to a scenario: a second
// model call scores the agent's output against Rubric on 0..1.
type JudgeConfig struct {
	Rubric   string  `json:"rubric"`             // what "good" means; the judge prompt
	MinScore float64 `json:"minScore,omitempty"` // pass threshold 0..1 (default 0.7)
	Model    string  `json:"model,omitempty"`    // model override (default: the agent's provider model)
}

// Scenario is one evaluable behaviour check.
type Scenario struct {
	Name   string       `json:"name"`
	Input  string       `json:"input"`
	Expect Expectation  `json:"expect"`
	Judge  *JudgeConfig `json:"judge,omitempty"`
}

// Result is the outcome of one scenario.
type Result struct {
	Name       string   `json:"name"`
	Pass       bool     `json:"pass"`
	Failures   []string `json:"failures,omitempty"`
	Output     string   `json:"output,omitempty"`
	Status     string   `json:"status,omitempty"`
	DurationMS int64    `json:"durationMs"`
	// Run observability: surfaced so evals also act as a cost/token
	// ledger for the agent.
	TokensIn   int     `json:"tokensIn,omitempty"`
	TokensOut  int     `json:"tokensOut,omitempty"`
	CostCents  float64 `json:"costCents,omitempty"`
	ToolCalls  int     `json:"toolCalls"`
	// Judge scoring, when the scenario has a judge configured.
	JudgeScore   float64 `json:"judgeScore,omitempty"`
	JudgeVerdict string  `json:"judgeVerdict,omitempty"` // pass | fail
	JudgeReason  string  `json:"judgeReason,omitempty"`
}

// Summary aggregates a run of scenarios: the top-level report emitted
// by the CLI and persisted as a regression baseline.
type Summary struct {
	Agent           string    `json:"agent"`
	Model           string    `json:"model,omitempty"`
	Scenarios       int       `json:"scenarios"`
	Passed          int       `json:"passed"`
	Failed          int       `json:"failed"`
	TotalCostCents  float64   `json:"totalCostCents"`
	TotalDurationMS int64     `json:"totalDurationMs"`
	Results         []*Result `json:"results"`
}

// JudgeVerdict is the parsed outcome of an LLM judge call.
type JudgeVerdict struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// Runner executes scenario inputs against an agent: in-process
// (AgentRunner) for `ernest eval`, or over HTTP (HTTPRunner) for
// `ernest replay` against a live production server. Provider returns
// the model provider used for judge scoring (the local config's
// provider in replay mode).
type Runner interface {
	RunScenario(ctx context.Context, input string) (*Outcome, error)
	Provider() llm.Provider
}

// Outcome is the observable result of running one scenario input: the
// same fields the deterministic checks inspect, whatever the transport.
type Outcome struct {
	Output      string
	Status      string
	Error       string // run-level error (e.g. runaway loop); reported as a failure
	ToolCalls   []core.ToolCall
	ToolResults []core.ToolResult
	Usage       *core.Usage
	CostCents   float64
	DurationMS  int64
}

// AgentRunner runs scenarios in-process against an agent.
type AgentRunner struct {
	Agent *agent.Agent
}

// Provider returns the agent's provider (judge scoring model).
func (r AgentRunner) Provider() llm.Provider { return r.Agent.Provider }

// RunScenario streams one input through the agent and collects the
// events the checks need. Runs are ephemeral (no session persistence).
func (r AgentRunner) RunScenario(ctx context.Context, input string) (*Outcome, error) {
	start := time.Now()
	ch, err := r.Agent.Stream(ctx, input, agent.RunOptions{SkipMemory: true})
	if err != nil {
		return nil, err
	}
	var o Outcome
	var res *core.RunResult
	var metrics *core.RunMetrics
	for ev := range ch {
		if ev.ToolCall != nil {
			o.ToolCalls = append(o.ToolCalls, *ev.ToolCall)
		}
		if ev.ToolResult != nil {
			o.ToolResults = append(o.ToolResults, *ev.ToolResult)
		}
		if ev.Metrics != nil {
			metrics = ev.Metrics
		}
		if ev.Result != nil {
			res = ev.Result
		}
	}
	if res == nil {
		return nil, errors.New("run produced no result")
	}
	o.Output = res.Output
	o.Status = string(res.Status)
	o.Error = res.Error
	o.Usage = res.Usage
	o.DurationMS = time.Since(start).Milliseconds()
	if o.DurationMS == 0 {
		o.DurationMS = res.DurationMS
	}
	if metrics != nil {
		o.CostCents = metrics.CostCents
	}
	return &o, nil
}

// Run executes one scenario against a runner and reports pass/fail.
func Run(ctx context.Context, r Runner, sc Scenario) (*Result, error) {
	start := time.Now()
	outcome, err := r.RunScenario(ctx, sc.Input)
	if err != nil {
		return nil, err
	}
	if outcome == nil {
		return nil, errors.New("runner returned no outcome")
	}
	outcome.DurationMS = time.Since(start).Milliseconds()
	res := &Result{
		Name:       sc.Name,
		Output:     outcome.Output,
		Status:     outcome.Status,
		DurationMS: outcome.DurationMS,
		ToolCalls:  len(outcome.ToolCalls),
	}
	if outcome.Usage != nil {
		res.TokensIn = outcome.Usage.InputTokens
		res.TokensOut = outcome.Usage.OutputTokens
	}
	res.CostCents = outcome.CostCents
	fail := func(format string, args ...any) {
		res.Failures = append(res.Failures, fmt.Sprintf(format, args...))
	}

	if sc.Expect.Status != "" && res.Status != sc.Expect.Status {
		fail("status = %s, want %s", res.Status, sc.Expect.Status)
	}
	if outcome.Error != "" {
		fail("run error: %s", outcome.Error)
	}
	for _, want := range sc.Expect.OutputContains {
		if !strings.Contains(res.Output, want) {
			fail("output %q does not contain %q", res.Output, want)
		}
	}
	if sc.Expect.NoToolCalls && len(outcome.ToolCalls) > 0 {
		fail("expected no tool calls, got %d (%s)", len(outcome.ToolCalls), outcome.ToolCalls[0].Name)
	}
	for _, want := range sc.Expect.ToolCalls {
		matched := false
		for _, c := range outcome.ToolCalls {
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
	for _, want := range sc.Expect.ToolResults {
		matched := false
		for _, tr := range outcome.ToolResults {
			if tr.Name != want.Name {
				continue
			}
			matched = true
			if want.ErrorContains != "" {
				switch {
				case tr.Error == "":
					fail("tool %s: expected failure containing %q, but it succeeded", want.Name, want.ErrorContains)
				case !strings.Contains(tr.Error, want.ErrorContains):
					fail("tool %s failed with %q, want contains %q", want.Name, tr.Error, want.ErrorContains)
				}
			}
			if want.Shape != nil {
				if tr.Error != "" {
					fail("tool %s failed: %s (shape checks need a successful result)", want.Name, tr.Error)
				} else {
					for _, m := range checkShape(tr.Content, want.Shape) {
						fail("tool %s: %s", want.Name, m)
					}
				}
			}
		}
		if !matched {
			fail("no tool result matching %s", want.Name)
		}
	}

	// LLM-as-judge: score the output against the rubric. The judge uses
	// the runner's provider so CI runs stay deterministic (script the
	// judge turn in the mock) and production runs use the same model
	// family as the agent (or the judge's own model override).
	if sc.Judge != nil {
		verdict, jerr := judge(ctx, r.Provider(), sc, res)
		if jerr != nil {
			fail("judge: %v", jerr)
		} else {
			res.JudgeScore = verdict.Score
			res.JudgeReason = verdict.Reason
			minScore := sc.Judge.MinScore
			if minScore <= 0 {
				minScore = 0.7
			}
			if verdict.Score >= minScore {
				res.JudgeVerdict = "pass"
			} else {
				res.JudgeVerdict = "fail"
				fail("judge score %.2f < min %.2f: %s", verdict.Score, minScore, verdict.Reason)
			}
		}
	}

	res.Pass = len(res.Failures) == 0
	return res, nil
}

// RunAll executes scenarios sequentially against the runner.
func RunAll(ctx context.Context, r Runner, scenarios []Scenario) ([]*Result, error) {
	out := make([]*Result, 0, len(scenarios))
	for _, sc := range scenarios {
		res, err := Run(ctx, r, sc)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: %w", sc.Name, err)
		}
		out = append(out, res)
	}
	return out, nil
}

// judge asks the provider to score the agent's output against the
// scenario rubric. The model is asked for strict JSON: {"score": 0..1,
// "reason": "..."} and anything else counts as a judge error.
func judge(ctx context.Context, p llm.Provider, sc Scenario, r *Result) (*JudgeVerdict, error) {
	if p == nil {
		return nil, errors.New("no provider on agent")
	}
	prompt := "You are a rigorous eval judge for an AI agent.\n" +
		"Score the agent's response against the rubric on a scale of 0.0 to 1.0.\n\n" +
		"TASK:\n" + sc.Input + "\n\n" +
		"RUBRIC:\n" + sc.Judge.Rubric + "\n\n" +
		"AGENT OUTPUT:\n" + r.Output + "\n\n" +
		`Reply with ONLY a JSON object, no prose: {"score": 0.0-1.0, "reason": "one short sentence"}`
	req := llm.ChatRequest{
		Model:       sc.Judge.Model,
		Messages:    []core.Message{core.NewUserMessage(prompt)},
		Temperature: floatPtr(0),
	}
	// No model override: judge with the agent's own model so mock-mode
	// evals stay deterministic and real evals judge in the same family.
	if req.Model == "" {
		req.Model = p.Model()
	}
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(resp.Content)
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("judge returned no JSON: %q", truncate(content, 200))
	}
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(content[start:end+1]), &v); err != nil {
		return nil, fmt.Errorf("judge JSON invalid: %v (got %q)", err, truncate(content, 200))
	}
	if v.Score < 0 || v.Score > 1 {
		return nil, fmt.Errorf("judge score %.2f out of range 0..1", v.Score)
	}
	return &v, nil
}

func floatPtr(f float64) *float64 { return &f }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Summarize aggregates results into a report.
func Summarize(agentName string, results []*Result) *Summary {
	s := &Summary{Agent: agentName, Results: results, Scenarios: len(results)}
	for _, r := range results {
		s.TotalDurationMS += r.DurationMS
		s.TotalCostCents += r.CostCents
		if r.Pass {
			s.Passed++
		} else {
			s.Failed++
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// baselines & regression
// ---------------------------------------------------------------------------

// Regression is one scenario's delta against a recorded baseline.
type Regression struct {
	Name       string  `json:"name"`
	WasPass    bool    `json:"wasPass"`
	NowPass    bool    `json:"nowPass"`
	ScoreDelta float64 `json:"scoreDelta,omitempty"` // judge score now - baseline
	New        bool    `json:"new,omitempty"`        // scenario not in baseline
	Note       string  `json:"note,omitempty"`
}

// BaselineFile is the on-disk shape of a baseline (a saved Summary).
type BaselineFile struct {
	Agent   string    `json:"agent,omitempty"`
	Results []*Result `json:"results"`
}

// SaveBaseline persists results as a baseline file for later diffs.
func SaveBaseline(path string, results []*Result) error {
	data, err := json.MarshalIndent(BaselineFile{Results: results}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadBaseline reads a baseline file. Both {"results": [...]} and a bare
// array are accepted.
func LoadBaseline(path string) ([]*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc BaselineFile
	if err := json.Unmarshal(data, &doc); err == nil && len(doc.Results) > 0 {
		return doc.Results, nil
	}
	var arr []*Result
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return arr, nil
}

// Regress diffs current results against a baseline. A scenario is a
// regression when it passed in the baseline and fails now. Judge-score
// moves of >= 0.25 (up or down) are reported as quality deltas even
// when the scenario still passes, so drift surfaces before it breaks.
// Unchanged scenarios are omitted from the diff.
func Regress(results, baseline []*Result) []Regression {
	base := make(map[string]*Result, len(baseline))
	for _, b := range baseline {
		base[b.Name] = b
	}
	var out []Regression
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.Name] = true
		b, ok := base[r.Name]
		if !ok {
			out = append(out, Regression{Name: r.Name, NowPass: r.Pass, New: true})
			continue
		}
		reg := Regression{Name: r.Name, WasPass: b.Pass, NowPass: r.Pass}
		if b.JudgeScore != 0 || r.JudgeScore != 0 {
			reg.ScoreDelta = r.JudgeScore - b.JudgeScore
		}
		switch {
		case b.Pass && !r.Pass:
			reg.Note = "passed in baseline, fails now"
		case !b.Pass && r.Pass:
			reg.Note = "now passing (was failing)"
		case reg.ScoreDelta <= -0.25:
			reg.Note = fmt.Sprintf("judge score dropped %.2f", reg.ScoreDelta)
		case reg.ScoreDelta >= 0.25:
			reg.Note = fmt.Sprintf("judge score improved %.2f", reg.ScoreDelta)
		default:
			continue // unchanged: omit from the diff
		}
		out = append(out, reg)
	}
	// Scenarios that existed in the baseline but are no longer in the
	// suite: a removed scenario can hide a regression, so report it.
	for name := range base {
		if !seen[name] {
			out = append(out, Regression{Name: name, WasPass: base[name].Pass, NowPass: false, Note: "scenario missing from current suite"})
		}
	}
	return out
}

// CountRegressions returns the number of true regressions (pass -> fail
// or disappeared scenarios). Score-only dips are reported but do not
// fail the gate.
func CountRegressions(regs []Regression) int {
	n := 0
	for _, r := range regs {
		if r.WasPass && !r.NowPass {
			n++
		}
	}
	return n
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
		if sc.Judge != nil && sc.Judge.Rubric == "" {
			return nil, fmt.Errorf("scenario %q: judge.rubric is required when judge is set", sc.Name)
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
		// Tool args from models vary in whitespace ("17 * 23" vs
		// "17*23"): compare strings whitespace-insensitively so
		// assertions survive real-model formatting.
		gs, gok := asString(got)
		ws, wok := asString(v)
		if gok && wok {
			if normalizeWS(gs) != normalizeWS(ws) {
				return false
			}
			continue
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", v) {
			return false
		}
	}
	return true
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func normalizeWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}
