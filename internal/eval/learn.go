package eval

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

// FailureRecord is one captured production failure: a run that failed,
// or a run in which a tool call failed. The server appends these to the
// file named by the config "failures" key (or `ernest run
// --failures-out`); `ernest eval --learn` turns them into scenarios.
type FailureRecord struct {
	RunID       string            `json:"runId,omitempty"`
	Agent       string            `json:"agent,omitempty"`
	Input       string            `json:"input"`
	Output      string            `json:"output,omitempty"`
	Status      string            `json:"status,omitempty"`
	Error       string            `json:"error,omitempty"`
	ToolCalls   []core.ToolCall   `json:"toolCalls,omitempty"`
	ToolResults []core.ToolResult `json:"toolResults,omitempty"`
	At          time.Time         `json:"at"`
}

// LoadFailures reads a JSONL file of FailureRecord entries.
func LoadFailures(path string) ([]FailureRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []FailureRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var rec FailureRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		if rec.Input == "" {
			continue // nothing to learn from without the prompt
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// fingerprint identifies one failure for deduplication: the normalized
// input plus the first tool error, so repeated failures of the same
// prompt (and the same broken tool) collapse into one scenario.
func fingerprint(rec FailureRecord) string {
	f := normalizeWS(rec.Input)
	if len(rec.ToolResults) > 0 {
		f += "|tool:" + rec.ToolResults[0].Name
		if rec.ToolResults[0].Error != "" {
			f += ":" + normalizeWS(rec.ToolResults[0].Error)
		}
	}
	if rec.Error != "" {
		f += "|" + normalizeWS(rec.Error)
	}
	sum := sha256.Sum256([]byte(f))
	return hex.EncodeToString(sum[:8])
}

// Learned is one generated scenario plus the failure record it came
// from (the CLI uses the record for judge rubric generation).
type Learned struct {
	Scenario Scenario
	Record   FailureRecord
}

// LearnFailure converts failure records into scenarios that would have
// caught them. Rules:
//
//   - a failed run becomes a scenario asserting status "completed";
//   - a tool that errored becomes a check that it fails with that
//     error (a tool that stops failing changes the agent's behaviour
//     and should be noticed);
//   - a tool that returned an empty array or empty string becomes a
//     shape check (minItems/minLength) — the silent 200 OK catcher;
//   - existing scenarios are the seen set: records whose normalized
//     input already has a scenario are skipped, so re-learning does
//     not duplicate the suite.
//
// The max limit caps generated scenarios per call (default 50).
func LearnFailure(records []FailureRecord, existing []Scenario, max int) []Learned {
	if max <= 0 {
		max = 50
	}
	seen := make(map[string]bool, len(existing))
	for _, sc := range existing {
		seen[normalizeWS(sc.Input)] = true
	}
	var out []Learned
	for _, rec := range records {
		if len(out) >= max {
			break
		}
		if seen[normalizeWS(rec.Input)] {
			continue
		}
		seen[normalizeWS(rec.Input)] = true
		out = append(out, Learned{Scenario: scenarioFromFailure(rec), Record: rec})
	}
	return out
}

// scenarioFromFailure derives one scenario from one failure record.
func scenarioFromFailure(rec FailureRecord) Scenario {
	sc := Scenario{
		Name:  learnName(rec),
		Input: rec.Input,
		Expect: Expectation{
			Status: "completed",
		},
	}
	if rec.Status == "failed" && rec.Error != "" {
		// The failure itself: the scenario asserts the run completes,
		// so the nightly replay flags it the moment the agent can't.
		sc.Expect.Status = "completed"
	}
	if len(rec.ToolResults) == 0 && rec.Error == "" && rec.Status == "completed" {
		// Nothing observable went wrong at the run level; keep the
		// scenario as a pure status check.
		return sc
	}
	sc.Expect.ToolResults = []ToolResultExpectation{}
	for _, tr := range rec.ToolResults {
		exp := ToolResultExpectation{Name: tr.Name}
		switch {
		case tr.Error != "":
			exp.ErrorContains = truncate(tr.Error, 120)
		case isEmptyArray(tr.Content):
			exp.Shape = &ShapeSpec{MinItems: 1}
		case isEmptyString(tr.Content):
			exp.Shape = &ShapeSpec{MinLength: 1}
		default:
			continue // no usable signal; don't over-assert
		}
		sc.Expect.ToolResults = append(sc.Expect.ToolResults, exp)
	}
	if len(sc.Expect.ToolResults) == 0 {
		sc.Expect.ToolResults = nil
	}
	return sc
}

func learnName(rec FailureRecord) string {
	words := strings.Fields(rec.Input)
	if len(words) > 6 {
		words = words[:6]
	}
	slug := strings.ToLower(strings.Join(words, "-"))
	var b strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 48 {
		name = name[:48]
	}
	if name == "" {
		name = "learned"
	}
	return name + "-" + fingerprint(rec)
}

func isEmptyArray(content json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return false
	}
	arr, ok := v.([]any)
	return ok && len(arr) == 0
}

func isEmptyString(content json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return false
	}
	s, ok := v.(string)
	return ok && s == ""
}

// GenerateRubric asks the provider to turn a production failure into a
// one-sentence judge rubric: what "handling this correctly" means now
// that we know how it fails. Used by `ernest eval --learn --judge`; the
// mock provider scripts it for deterministic CI.
func GenerateRubric(ctx context.Context, p llm.Provider, rec FailureRecord) (string, error) {
	if p == nil {
		return "", fmt.Errorf("no provider for rubric generation")
	}
	detail := rec.Input
	if rec.Error != "" {
		detail += "\n\nRUN ERROR: " + rec.Error
	}
	if rec.Output != "" {
		detail += "\n\nOUTPUT: " + truncate(rec.Output, 500)
	}
	for _, tr := range rec.ToolResults {
		detail += fmt.Sprintf("\nTOOL %s: error=%q content=%s", tr.Name, tr.Error, truncate(string(tr.Content), 300))
	}
	prompt := "You are turning a real production failure into an eval rubric for an AI agent.\n\n" +
		"FAILURE:\n" + detail + "\n\n" +
		`Write ONE sentence describing what a correct response to the user input looks like, in terms a grader can check without seeing the failure. Reply with only the rubric sentence.`
	req := llm.ChatRequest{
		Model:       p.Model(),
		Messages:    []core.Message{core.NewUserMessage(prompt)},
		Temperature: floatPtr(0),
	}
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	rubric := strings.TrimSpace(resp.Content)
	if rubric == "" {
		return "", fmt.Errorf("rubric generation returned empty text")
	}
	return rubric, nil
}
