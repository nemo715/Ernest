// Research Lab — a PhD-level multi-agent research team built on ernest's
// public API (github.com/nemo715/Ernest/...), with a live web dashboard.
//
//   - Real mode:   set OPENROUTER_API_KEY and every agent runs on
//                  openai/gpt-4o-mini via OpenRouter; the Principal
//                  Investigator decides who to delegate to.
//   - Offline mode: without a key, scripted mock providers demo the same
//                  delegation flow deterministically (no network, no cost).
//
// Run: go run .   then open http://127.0.0.1:8080
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
	"github.com/nemo715/Ernest/server"
	"github.com/nemo715/Ernest/storage"
	"github.com/nemo715/Ernest/team"
)

const (
	addr     = "127.0.0.1:8080"
	baseURL  = "https://openrouter.ai/api/v1"
	model    = "openai/gpt-4o-mini"
	apiKeyID = "OPENROUTER_API_KEY"
)

func main() {
	mode := "mock"
	providers := mockProviders()
	if key := os.Getenv(apiKeyID); key != "" {
		mode = "real"
		providers = realProviders(key)
	}

	// --- The research team -------------------------------------------
	lead := agent.New("lead", providers["lead"])
	lead.Description = "Principal Investigator — plans, delegates, synthesizes"
	lead.Instructions = `You are the Principal Investigator of a research lab.
Your job: take a research question, plan an investigation, delegate each
specialist task to the right member via the delegate tool, then synthesize
their findings into one final, well-structured answer.
- Use delegate for reviewer (literature & evidence), analyst (numbers &
  verification) and writer (polished prose) as appropriate.
- State your reasoning briefly before each delegation.
- The final answer must cite the members you consulted and flag uncertainty.`
	lead.Tools = core.BuiltinTools

	reviewer := agent.New("reviewer", providers["reviewer"])
	reviewer.Description = "Literature & Evidence — verifies claims, finds sources"
	reviewer.Instructions = `You are a PhD-level literature reviewer.
- Verify every claim; prefer primary sources; use http_fetch when a source
  is needed. Note the source and date for each finding.
- Report findings as a numbered list, marking confidence: HIGH / MEDIUM / LOW.
- Never fabricate citations: if you did not check a source, say so.`

	analyst := agent.New("analyst", providers["analyst"])
	analyst.Description = "Quantitative Analysis — exact math, cost/scale checks"
	analyst.Instructions = `You are a quantitative research analyst.
- Solve all arithmetic exactly with the calculator tool; never estimate sums.
- Check claims for internal consistency (units, orders of magnitude).
- Report results with the method used and the numbers you verified.`

	writer := agent.New("writer", providers["writer"])
	writer.Description = "Academic Writing — turns findings into polished prose"
	writer.Instructions = `You are an academic writer.
- Produce clear, well-structured prose with markdown headings.
- Only use facts provided by the reviewer/analyst; do not invent numbers.
- Keep the final answer focused and readable (max ~300 words unless asked).`

	lab := team.New("research-lab", lead, reviewer, analyst, writer)

	// --- HTTP API + dashboard ----------------------------------------
	srv, err := server.New(server.Options{
		Agents: []*agent.Agent{lead, reviewer, analyst, writer},
		Store:  storage.NewInMemoryStore(),
		Static: "ui",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
	defer srv.Close()

	app := &app{team: lab, mode: mode}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/team/run", app.handleTeamRun) // SSE: ?task=...
	mux.HandleFunc("/api/team/info", app.handleTeamInfo)
	mux.Handle("/", srv.Handler())

	fmt.Printf("research-lab (%s mode): http://%s  — team: lead, reviewer, analyst, writer\n", mode, addr)
	fmt.Println("press ctrl-c to stop")
	http.ListenAndServe(addr, mux)
}

type app struct {
	team *team.Team
	mode string
	mu   sync.Mutex // serialize runs so the feed stays readable
}

// handleTeamRun streams a team run as server-sent events.
// Every event is sent as `event: run` + a JSON RunEvent payload; the UI
// dispatches on payload.type (run.start, delegate.start, message.delta,
// tool.call, delegate.end, run.complete, ...).
func (a *app) handleTeamRun(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	if task == "" {
		http.Error(w, "missing ?task=", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "event: meta\ndata: {\"mode\":\"%s\",\"task\":%q}\n\n", a.mode, task)
	fl.Flush()

	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ch, err := a.team.Stream(ctx, task, team.RunOptions{})
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", jsonEscape(err.Error()))
		fl.Flush()
		return
	}
	enc := json.NewEncoder(w)
	for ev := range ch {
		fmt.Fprint(w, "event: run\n")
		fmt.Fprint(w, "data: ")
		if err := enc.Encode(ev); err != nil {
			return
		}
		fmt.Fprint(w, "\n")
		fl.Flush()
	}
	fmt.Fprint(w, "event: done\n\n")
	fl.Flush()
}

func (a *app) handleTeamInfo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"mode":  a.mode,
		"model": model,
		"agents": []map[string]string{
			{"name": "lead", "role": "Principal Investigator"},
			{"name": "reviewer", "role": "Literature & Evidence"},
			{"name": "analyst", "role": "Quantitative Analysis"},
			{"name": "writer", "role": "Academic Writing"},
		},
	})
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- providers --------------------------------------------------------

func realProviders(key string) map[string]llm.Provider {
	cfg := llm.OpenAICompatConfig{BaseURL: baseURL, APIKey: key, Model: model}
	return map[string]llm.Provider{
		"lead":     llm.NewOpenAICompat(cfg),
		"reviewer": llm.NewOpenAICompat(cfg),
		"analyst":  llm.NewOpenAICompat(cfg),
		"writer":   llm.NewOpenAICompat(cfg),
	}
}

// mockProviders script a deterministic offline demo: the lead delegates to
// the reviewer, then the analyst, then streams a synthesis.
func mockProviders() map[string]llm.Provider {
	return map[string]llm.Provider{
		"lead": llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{{
				Name: "delegate",
				Arguments: json.RawMessage(
					`{"member":"reviewer","task":"Review the literature on the research question and report findings with confidence levels."}`),
			}}},
			{ToolCalls: []core.ToolCall{{
				Name: "delegate",
				Arguments: json.RawMessage(
					`{"member":"analyst","task":"Check the numbers and quantities in the findings for consistency."}`),
			}}},
			{Content: "Synthesis: the reviewer found that the core claims hold with HIGH confidence across the checked sources; the analyst verified the figures are internally consistent. The complete answer follows the delegation trail above.", FinishReason: "stop"},
		}}),
		"reviewer": llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{Content: "Findings (HIGH confidence):\n1. The core mechanism is well documented in the primary sources.\n2. Reported effect sizes are consistent across replications.\n3. One secondary source disagrees — flagged as MEDIUM confidence.", FinishReason: "stop"},
		}}),
		"analyst": llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{Content: "Analysis: all quantities re-computed with the calculator are internally consistent (no unit errors, orders of magnitude correct).", FinishReason: "stop"},
		}}),
		"writer": llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{Content: "Draft ready: structured summary attached.", FinishReason: "stop"},
		}}),
	}
}
