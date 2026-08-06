// Command ernest is the ernest CLI: scaffold projects (init), run agents
// (run), boot the playground (playground) and diagnose environments
// (doctor).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/config"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/eval"
	"github.com/nemo715/Ernest/internal/mcp"
	"github.com/nemo715/Ernest/internal/server"
	"github.com/nemo715/Ernest/internal/storage"
)

const version = "0.1.5"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ernest:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "new":
		return cmdNew(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "playground":
		return cmdPlayground(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "eval":
		return cmdEval(args[1:])
	case "replay":
		return cmdReplay(args[1:])
	case "mcp-serve":
		return cmdMCPServe(args[1:])
	case "version", "--version", "-v":
		fmt.Println("ernest " + version)
		return nil
	case "help", "--help", "-h", "":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (init|new|run|eval|replay|mcp-serve|playground|doctor)", args[0])
	}
}

func usage() {
	fmt.Print(`ernest — the fastest multi-agent framework

Usage:
  ernest init [dir]          scaffold an agent project (ernest.json, .env.example, main.go)
  ernest new <template> [dir]  scaffold a project from a template (agent|team|workflow|server)
  ernest run [flags]         run an agent from ernest.json
  ernest eval [flags]        run scenario evals against an agent
  ernest replay [flags]      replay the eval suite against a live server (nightly drift)
  ernest mcp-serve [flags]   expose agents as MCP tools over stdio
  ernest playground [flags]  boot the playground server (web UI backend)
  ernest doctor [flags]      diagnose the environment and configuration
  ernest version             print the version
  ernest help                show this help

Run "ernest <command> -h" for command flags.
`)
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

const scaffoldConfig = `{
  "agents": [
    {
      "name": "assistant",
      "description": "General assistant",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You are a helpful assistant.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`

const scaffoldEnv = `# Copy to .env and fill in the providers you use.
# ernest reads keys from these variables at runtime.
OPENAI_API_KEY=
ANTHROPIC_API_KEY=
GEMINI_API_KEY=
GROQ_API_KEY=
`

const scaffoldMain = `package main

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
)

// A minimal programmatic agent. Swap the mock provider for a real one:
//
//	p := llm.OpenAI(os.Getenv("OPENAI_API_KEY"), "gpt-4o-mini")
func main() {
	p := llm.NewMock(llm.MockConfig{})
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant."
	a.Tools = core.BuiltinTools
	res, err := a.Chat(context.Background(), "Say hello")
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Output)
}
`

// scaffoldMod is the go.mod written by init and every `ernest new`
// template: the project is its own module and imports ernest from the
// published module path, so scaffolds compile outside the ernest repo.
const scaffoldMod = "module myapp\n\ngo 1.26.5\n\nrequire github.com/nemo715/Ernest v0.1.5\n"

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: ernest init [dir]")
		fmt.Println("Scaffolds ernest.json, .env.example and main.go in dir (default .).")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"ernest.json":   scaffoldConfig,
		".env.example":  scaffoldEnv,
		"main.go":       scaffoldMain,
		"go.mod":        scaffoldMod,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("skip %s (already exists)\n", name)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("created %s\n", path)
	}
	fmt.Println("\nNext: edit ernest.json, then `go mod tidy && go run .` or `ernest run` / `ernest playground`.")
	return nil
}

// ---------------------------------------------------------------------------
// new (templates)
// ---------------------------------------------------------------------------

// newTemplates are the `ernest new <template>` scaffolds. Every template
// writes a valid ernest.json (config.Validate), a compiling main.go and a
// .env.example; providers default to mock so projects run without keys.
var newTemplates = map[string]struct {
	summary string
	files   map[string]string
}{
	"agent": {
		summary: "single agent with tools + memory",
		files: map[string]string{
			"go.mod":      scaffoldMod,
			"ernest.json": `{
  "agents": [
    {
      "name": "assistant",
      "description": "General assistant with calculator, fetch and time tools",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You are a helpful assistant. Use tools when they help.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`,
			"main.go": `package main

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
)

// A minimal programmatic agent. Swap the mock provider for a real one:
//
//	p := llm.OpenAI(os.Getenv("OPENAI_API_KEY"), "gpt-4o-mini")
func main() {
	p := llm.NewMock(llm.MockConfig{Stream: true})
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant."
	a.Tools = core.BuiltinTools

	// Stream the run event by event.
	ch, err := a.Stream(context.Background(), "Say hello", agent.RunOptions{})
	if err != nil {
		panic(err)
	}
	for ev := range ch {
		if ev.Type == core.EventMessageDelta {
			fmt.Print(ev.Delta)
		}
	}
	fmt.Println()
}
`,
		},
	},
	"team": {
		summary: "leader agent delegating to specialist members",
		files: map[string]string{
			"go.mod":      scaffoldMod,
			"ernest.json": `{
  "agents": [
    {
      "name": "lead",
      "description": "Team leader that delegates to specialists",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You coordinate the team and delegate tasks.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true
    },
    {
      "name": "researcher",
      "description": "Finds facts and figures",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You research topics and report findings.",
      "tools": ["http_fetch", "now"]
    },
    {
      "name": "writer",
      "description": "Turns notes into polished text",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You write clear, concise prose.",
      "tools": []
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`,
			"main.go": `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
	"github.com/nemo715/Ernest/team"
)

// A multi-agent team: the leader decides when to delegate, using the
// delegate tool that team.New injects automatically.
//
// With OPENROUTER_API_KEY set, the team runs on a real model
// (gpt-4o-mini via OpenRouter) and the model decides delegation itself.
// Without a key it falls back to scripted mock providers, so the scaffold
// also runs offline and deterministically for demos and CI.
func main() {
	var leadP, researchP, writerP llm.Provider
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		cfg := llm.OpenAICompatConfig{
			BaseURL: "https://openrouter.ai/api/v1",
			APIKey:  key,
			Model:   "openai/gpt-4o-mini",
		}
		leadP = llm.NewOpenAICompat(cfg)
		researchP = llm.NewOpenAICompat(cfg)
		writerP = llm.NewOpenAICompat(cfg)
	} else {
		fmt.Println("· no OPENROUTER_API_KEY — scripted mock demo (set the key for a real model)")
		leadP = llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{{
				Name: "delegate",
				Arguments: json.RawMessage("{\"member\":\"researcher\",\"task\":\"Research Go concurrency and report your findings.\"}"),
			}}},
			{Content: "Done. I delegated the research and synthesised the findings into a short summary.", FinishReason: "stop"},
		}})
		researchP = llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
			{Content: "Findings: Go concurrency is built on goroutines (cheap, multiplexed threads) and channels for safe communication; 'go f()' starts a goroutine, 'select' coordinates many.", FinishReason: "stop"},
		}})
		writerP = llm.NewMock(llm.MockConfig{})
	}

	leader := agent.New("lead", leadP)
	leader.Instructions = "You coordinate the team and delegate tasks."
	leader.Tools = core.BuiltinTools

	researcherAgent := agent.New("researcher", researchP)
	researcherAgent.Description = "Finds facts and figures"
	researcherAgent.Instructions = "You research topics and report findings."

	writerAgent := agent.New("writer", writerP)
	writerAgent.Description = "Turns notes into polished text"
	writerAgent.Instructions = "You write clear, concise prose."

	t := team.New("editorial", leader, researcherAgent, writerAgent)
	ch, err := t.Stream(context.Background(), "Research Go concurrency and write a short summary.", team.RunOptions{})
	if err != nil {
		panic(err)
	}
	for ev := range ch {
		switch ev.Type {
		case core.EventDelegateStart:
			fmt.Printf("· delegate → %s: %s\n", ev.Agent, ev.Data)
		case core.EventDelegateEnd:
			fmt.Printf("· delegate ← %s done\n", ev.Agent)
		case core.EventMessageDelta:
			fmt.Print(ev.Delta)
		case core.EventRunComplete:
			fmt.Printf("\n· run.complete: %s\n", ev.Result.Status)
		}
	}
}
`,
		},
	},
	"workflow": {
		summary: "explicit step DAG with guards, retries and agents",
		files: map[string]string{
			"go.mod":      scaffoldMod,
			"ernest.json": `{
  "agents": [
    {
      "name": "assistant",
      "description": "Workflow worker agent",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You are a precise worker.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`,
			"main.go": `package main

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/llm"
	"github.com/nemo715/Ernest/workflow"
)

// A step DAG: plan -> research (via the agent) -> write. Steps share
// state; independent steps would run concurrently.
func main() {
	p := llm.NewMock(llm.MockConfig{})
	a := agent.New("assistant", p)
	a.Instructions = "You are a precise worker."

	wf := workflow.New("pipeline")
	wf.Agents = map[string]*agent.Agent{"assistant": a}
	wf.Steps = []*workflow.Step{
		{
			Name: "plan",
			Run: func(ctx *workflow.StepContext) error {
				ctx.Log("planning %v", ctx.Input())
				ctx.Set("plan", "research -> write")
				return nil
			},
		},
		{
			Name:      "research",
			DependsOn: []string{"plan"},
			Run: func(ctx *workflow.StepContext) error {
				res, err := ctx.Agent("assistant").Chat(ctx.Ctx, "Research: Go concurrency",
					agent.RunOptions{SessionID: ctx.RunID + ":research"})
				if err != nil {
					return err
				}
				ctx.Set("notes", res.Output)
				return nil
			},
		},
		{
			Name:      "write",
			DependsOn: []string{"research"},
			Run: func(ctx *workflow.StepContext) error {
				fmt.Println("notes:", ctx.Get("notes"))
				return nil
			},
		},
	}

	res, err := wf.Run(context.Background(), "a two-paragraph intro to Go")
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Output)
}
`,
		},
	},
	"server": {
		summary: "HTTP API + playground backend (SSE, WS, HITL)",
		files: map[string]string{
			"go.mod":      scaffoldMod,
			"ernest.json": `{
  "agents": [
    {
      "name": "assistant",
      "description": "General assistant served over HTTP",
      "provider": "mock",
      "model": "mock-1",
      "instructions": "You are a helpful assistant. Use tools when they help.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`,
			"main.go": `package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
	"github.com/nemo715/Ernest/server"
	"github.com/nemo715/Ernest/storage"
)

// An embedded HTTP server exposing the full API: /api/chat (SSE),
// /ws/chat (realtime transport), approvals, sessions, runs/trace.
func main() {
	p := llm.NewMock(llm.MockConfig{})
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant."
	a.Tools = core.BuiltinTools
	a.Store = storage.NewInMemoryStore()

	srv, err := server.New(server.Options{Agents: []*agent.Agent{a}, Store: a.Store})
	if err != nil {
		panic(err)
	}
	defer srv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:9090")
	if err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	fmt.Println("ernest server: http://" + ln.Addr().String())
	httpServer := &http.Server{Handler: srv.Handler()}
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
`,
		},
	},
}

func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: ernest new <template> [dir]")
		fmt.Println("Scaffolds a project from a template into dir (default .).")
		fmt.Println("Templates:")
		for name, t := range newTemplates {
			fmt.Printf("  %-9s %s\n", name, t.summary)
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return nil
	}
	tpl, ok := newTemplates[fs.Arg(0)]
	if !ok {
		names := make([]string, 0, len(newTemplates))
		for n := range newTemplates {
			names = append(names, n)
		}
		return fmt.Errorf("unknown template %q (available: %s)", fs.Arg(0), strings.Join(names, ", "))
	}
	dir := "."
	if fs.NArg() > 1 {
		dir = fs.Arg(1)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range tpl.files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("skip %s (already exists)\n", path)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("created %s\n", path)
	}
	fmt.Println("\nNext: cd " + dir + " && go mod tidy && go run .  (or ernest playground; mock provider needs no keys)")
	return nil
}

// ---------------------------------------------------------------------------
// run
// ---------------------------------------------------------------------------

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json")
	agentName := fs.String("agent", "", "agent name (default: first agent)")
	input := fs.String("input", "", "one-shot input; without it ernest reads lines from stdin")
	session := fs.String("session", "", "session id for memory continuity")
	asJSON := fs.Bool("json", false, "print the full run result as JSON")
	noMemory := fs.Bool("no-memory", false, "skip session persistence")
	failuresOut := fs.String("failures-out", "", "append failed runs to this JSONL file (feed for `ernest eval --learn`)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	rt, err := cfg.Build(nil)
	if err != nil {
		return err
	}
	defer rt.Close()

	ag := pickAgent(rt.Agents, *agentName)
	if ag == nil {
		return fmt.Errorf("agent %q not found (have: %s)", *agentName, agentNames(rt.Agents))
	}

	ctx := context.Background()
	if *input != "" {
		res, err := runOnce(ctx, ag, *input, *session, *noMemory)
		if err != nil {
			return err
		}
		if res.Status == core.RunStatusFailed && *failuresOut != "" {
			if err := appendFailureFile(*failuresOut, failureRecordFromResult(res, ag.Name)); err != nil {
				return fmt.Errorf("failures-out: %w", err)
			}
			fmt.Fprintf(os.Stderr, "failure recorded for eval --learn: %s\n", *failuresOut)
		}
		return printResult(res, *asJSON)
	}
	// Interactive: read lines from stdin, stream output per line.
	sc := bufio.NewScanner(os.Stdin)
	fmt.Printf("ernest> %s (ctrl-c to quit)\n", ag.Name)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		res, err := runOnce(ctx, ag, line, *session, *noMemory)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		if res.Status == core.RunStatusFailed && *failuresOut != "" {
			_ = appendFailureFile(*failuresOut, failureRecordFromResult(res, ag.Name))
		}
		if res.Status == "awaiting_approval" {
			for _, ap := range res.Approvals {
				fmt.Printf("[approval needed] %s: %s (resume via UI or SDK)\n", ap.Action, ap.Summary)
			}
		}
		fmt.Println(res.Output)
	}
	return sc.Err()
}

// failureRecordFromResult builds a FailureRecord from a failed run
// result. Tool results are rebuilt from the tool messages (the CLI has
// no event stream); error strings are not persisted in messages, so
// shape inference mostly applies to the run-level failure.
func failureRecordFromResult(res *core.RunResult, agentName string) eval.FailureRecord {
	rec := eval.FailureRecord{
		RunID:  res.RunID,
		Agent:  agentName,
		Output: res.Output,
		Status: string(res.Status),
		Error:  res.Error,
		At:     time.Now(),
	}
	for _, m := range res.Messages {
		if rec.Input == "" && m.Role == core.RoleUser && m.Content != "" {
			rec.Input = m.Content
		}
		if m.Role == core.RoleAssistant {
			rec.ToolCalls = append(rec.ToolCalls, m.ToolCalls...)
		}
		if m.Role == core.RoleTool {
			content, _ := json.Marshal(m.Content)
			rec.ToolResults = append(rec.ToolResults, core.ToolResult{ID: m.ToolCallID, Name: m.Name, Content: content})
		}
	}
	return rec
}

// appendFailureFile appends one FailureRecord line to a JSONL file.
func appendFailureFile(path string, rec eval.FailureRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func runOnce(ctx context.Context, ag *agent.Agent, input, session string, noMemory bool) (*core.RunResult, error) {
	opts := agent.RunOptions{SessionID: session, SkipMemory: noMemory}
	return ag.Chat(ctx, input, opts)
}

func printResult(res *core.RunResult, asJSON bool) error {
	if asJSON {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if res.Status != core.RunStatusCompleted {
		fmt.Printf("[%s] ", res.Status)
	}
	fmt.Println(res.Output)
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	return nil
}

func pickAgent(agents []*agent.Agent, name string) *agent.Agent {
	if name == "" && len(agents) > 0 {
		return agents[0]
	}
	for _, a := range agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func agentNames(agents []*agent.Agent) string {
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// eval
// ---------------------------------------------------------------------------

func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json")
	agentName := fs.String("agent", "", "agent name (default: first agent)")
	scenarios := fs.String("scenarios", "scenarios", "scenario file or directory")
	asJSON := fs.Bool("json", false, "print the full summary as JSON")
	baseline := fs.String("baseline", "", "compare against a saved baseline; regressions exit non-zero")
	updateBaseline := fs.Bool("update-baseline", false, "save this run as the baseline (default file: eval-baseline.json)")
	learn := fs.String("learn", "", "learn scenarios from a failures JSONL file (the server's failures feed); generated scenarios are added to the scenarios dir and evaluated in the same run")
	learnMax := fs.Int("learn-max", 50, "max scenarios to learn in one run")
	learnJudge := fs.Bool("learn-judge", false, "generate an LLM judge rubric per learned scenario (costs tokens)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	rt, err := cfg.Build(nil)
	if err != nil {
		return err
	}
	defer rt.Close()

	ag := pickAgent(rt.Agents, *agentName)
	if ag == nil {
		return fmt.Errorf("agent %q not found (have: %s)", *agentName, agentNames(rt.Agents))
	}

	ctx := context.Background()
	if *learn != "" {
		if err := learnScenarios(ctx, ag, *learn, *scenarios, *learnMax, *learnJudge); err != nil {
			return err
		}
	}

	scs, err := eval.LoadScenarios(*scenarios)
	if err != nil {
		return fmt.Errorf("scenarios: %w", err)
	}
	if len(scs) == 0 {
		return fmt.Errorf("no scenarios found in %s", *scenarios)
	}

	results, err := eval.RunAll(ctx, eval.AgentRunner{Agent: ag}, scs)
	if err != nil {
		return err
	}
	sum := eval.Summarize(ag.Name, results)
	sum.Model = ag.Provider.Model()

	// Regression gate: compare against a saved baseline.
	var regs []eval.Regression
	baselinePath := *baseline
	if *updateBaseline {
		if baselinePath == "" {
			baselinePath = "eval-baseline.json"
		}
		if err := eval.SaveBaseline(baselinePath, results); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		fmt.Printf("baseline written to %s (%d scenarios)\n\n", baselinePath, len(results))
	}
	if baselinePath != "" && !*updateBaseline {
		base, err := eval.LoadBaseline(baselinePath)
		if err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		regs = eval.Regress(results, base)
	}

	if *asJSON {
		type report struct {
			*eval.Summary
			Regressions []eval.Regression `json:"regressions,omitempty"`
		}
		out := report{Summary: sum, Regressions: regs}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		fmt.Printf("ernest eval — agent %s (%s), %d scenarios\n", sum.Agent, sum.Model, sum.Scenarios)
		for _, r := range results {
			mark := "PASS"
			if !r.Pass {
				mark = "FAIL"
			}
			line := fmt.Sprintf("  %-4s %-30s %5dms", mark, r.Name, r.DurationMS)
			if r.TokensIn+r.TokensOut > 0 {
				line += fmt.Sprintf("  %dtok", r.TokensIn+r.TokensOut)
			}
			if r.CostCents > 0 {
				line += fmt.Sprintf("  $%.4f", r.CostCents/100)
			}
			if r.JudgeScore > 0 {
				line += fmt.Sprintf("  judge %.2f [%s]", r.JudgeScore, r.JudgeVerdict)
			}
			fmt.Println(line)
			if !r.Pass {
				for _, f := range r.Failures {
					fmt.Printf("        - %s\n", f)
				}
			}
		}
		fmt.Printf("\n%d/%d passed  %.2fms total  $%.4f\n", sum.Passed, sum.Scenarios, float64(sum.TotalDurationMS), sum.TotalCostCents/100)
		if len(regs) > 0 {
			fmt.Printf("\nregression vs %s:\n", baselinePath)
			for _, r := range regs {
				if r.New {
					fmt.Printf("  + %-30s new scenario (pass=%v)\n", r.Name, r.NowPass)
					continue
				}
				line := fmt.Sprintf("  %s %-30s was %s now %s", "=", r.Name, markOf(r.WasPass), markOf(r.NowPass))
				if r.ScoreDelta != 0 {
					line += fmt.Sprintf("  judge %+.2f", r.ScoreDelta)
				}
				if r.Note != "" {
					line += "  <-- " + r.Note
				}
				fmt.Println(line)
			}
		}
		if n := eval.CountRegressions(regs); n > 0 {
			return fmt.Errorf("%d regression(s) vs baseline %s", n, baselinePath)
		}
	}
	// The gate applies in both output modes: failed scenarios or
	// regressions always exit non-zero so CI sees a failed eval.
	if sum.Failed > 0 {
		return fmt.Errorf("%d scenario(s) failed", sum.Failed)
	}
	if n := eval.CountRegressions(regs); n > 0 {
		return fmt.Errorf("%d regression(s) vs baseline %s", n, baselinePath)
	}
	return nil
}

func markOf(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

// learnScenarios turns production failures into scenarios: reads the
// failures JSONL, dedupes against the current suite, optionally writes
// LLM judge rubrics, merges into <scenarios-dir>/generated.json and
// reports what was learned.
func learnScenarios(ctx context.Context, ag *agent.Agent, failuresPath, scenariosPath string, max int, withJudge bool) error {
	recs, err := eval.LoadFailures(failuresPath)
	if err != nil {
		return fmt.Errorf("learn: %w", err)
	}
	if len(recs) == 0 {
		return fmt.Errorf("learn: no failure records in %s", failuresPath)
	}
	existing, err := eval.LoadScenarios(scenariosPath)
	if err != nil {
		return fmt.Errorf("learn: scenarios: %w", err)
	}
	learned := eval.LearnFailure(recs, existing, max)
	if len(learned) == 0 {
		fmt.Printf("learn: %d failure record(s) already covered by the suite — nothing new\n", len(recs))
		return nil
	}
	scs := make([]eval.Scenario, 0, len(learned))
	if withJudge {
		for i := range learned {
			rubric, err := eval.GenerateRubric(ctx, ag.Provider, learned[i].Record)
			if err != nil {
				return fmt.Errorf("learn: rubric for %q: %w", learned[i].Scenario.Name, err)
			}
			learned[i].Scenario.Judge = &eval.JudgeConfig{Rubric: rubric}
			scs = append(scs, learned[i].Scenario)
		}
	} else {
		for i := range learned {
			scs = append(scs, learned[i].Scenario)
		}
	}
	if err := appendGeneratedScenarios(scenariosPath, scs); err != nil {
		return fmt.Errorf("learn: write generated scenarios: %w", err)
	}
	fmt.Printf("learn: %d new scenario(s) from %d failure record(s) → %s (evaluated below)\n", len(scs), len(recs), generatedPath(scenariosPath))
	return nil
}

// appendGeneratedScenarios merges learned scenarios into the
// generated.json next to the scenarios file/dir, keeping prior learned
// scenarios (re-learning dedupes against them).
func appendGeneratedScenarios(scenariosPath string, scs []eval.Scenario) error {
	out := generatedPath(scenariosPath)
	var existing []eval.Scenario
	if data, err := os.ReadFile(out); err == nil {
		var doc struct {
			Scenarios []eval.Scenario `json:"scenarios"`
		}
		if err := json.Unmarshal(data, &doc); err == nil {
			existing = doc.Scenarios
		}
	}
	all := append(existing, scs...)
	data, err := json.MarshalIndent(map[string]any{"scenarios": all}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// generatedPath is where learned scenarios live: next to the suite.
func generatedPath(scenariosPath string) string {
	dir := filepath.Dir(scenariosPath)
	return filepath.Join(dir, "generated.json")
}

// ---------------------------------------------------------------------------
// replay
// ---------------------------------------------------------------------------

// cmdReplay runs the eval suite against a LIVE ernest server (nightly
// drift monitoring): same assertions and baselines as `ernest eval`,
// but every scenario executes over HTTP against the deployed agent.
// Judge scoring uses the local config's provider, so the suite still
// runs offline evals in the deployment's model family.
func cmdReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json (judge provider)")
	agentName := fs.String("agent", "", "agent name on the server (default: first agent)")
	scenarios := fs.String("scenarios", "scenarios", "scenario file or directory")
	endpoint := fs.String("endpoint", "", "live ernest server base URL (required)")
	baseline := fs.String("baseline", "", "compare against a saved baseline; regressions exit non-zero")
	updateBaseline := fs.Bool("update-baseline", false, "save this run as the baseline (default file: eval-baseline.json)")
	webhook := fs.String("webhook", "", "POST the drift report to this URL when the replay finishes")
	timeoutSec := fs.Int("timeout", 120, "per-scenario timeout in seconds")
	asJSON := fs.Bool("json", false, "print the full report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" {
		return fmt.Errorf("replay: --endpoint is required (e.g. http://prod:9090)")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	rt, err := cfg.Build(nil)
	if err != nil {
		return err
	}
	defer rt.Close()

	ag := pickAgent(rt.Agents, *agentName)
	if ag == nil {
		return fmt.Errorf("agent %q not found (have: %s)", *agentName, agentNames(rt.Agents))
	}

	scs, err := eval.LoadScenarios(*scenarios)
	if err != nil {
		return fmt.Errorf("scenarios: %w", err)
	}
	if len(scs) == 0 {
		return fmt.Errorf("no scenarios found in %s", *scenarios)
	}

	ctx := context.Background()
	runner := eval.HTTPRunner{
		Endpoint:      *endpoint,
		Agent:         ag.Name,
		JudgeProvider: ag.Provider,
		Timeout:       time.Duration(*timeoutSec) * time.Second,
	}
	results, err := eval.RunAll(ctx, runner, scs)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	sum := eval.Summarize(ag.Name, results)
	sum.Model = runner.Model(ctx)

	// Regression gate: compare against a saved baseline.
	var regs []eval.Regression
	baselinePath := *baseline
	if *updateBaseline {
		if baselinePath == "" {
			baselinePath = "eval-baseline.json"
		}
		if err := eval.SaveBaseline(baselinePath, results); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		fmt.Printf("baseline written to %s (%d scenarios)\n\n", baselinePath, len(results))
	}
	if baselinePath != "" && !*updateBaseline {
		base, err := eval.LoadBaseline(baselinePath)
		if err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		regs = eval.Regress(results, base)
	}

	type report struct {
		*eval.Summary
		Endpoint    string            `json:"endpoint"`
		Regressions []eval.Regression `json:"regressions,omitempty"`
	}
	out := report{Summary: sum, Endpoint: *endpoint, Regressions: regs}
	if *asJSON {
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		model := sum.Model
		if model == "" {
			model = "?"
		}
		fmt.Printf("ernest replay — agent %s (%s) @ %s, %d scenarios\n", sum.Agent, model, *endpoint, sum.Scenarios)
		for _, r := range results {
			mark := "PASS"
			if !r.Pass {
				mark = "FAIL"
			}
			line := fmt.Sprintf("  %-4s %-30s %5dms", mark, r.Name, r.DurationMS)
			if r.TokensIn+r.TokensOut > 0 {
				line += fmt.Sprintf("  %dtok", r.TokensIn+r.TokensOut)
			}
			if r.CostCents > 0 {
				line += fmt.Sprintf("  $%.4f", r.CostCents/100)
			}
			if r.JudgeScore > 0 {
				line += fmt.Sprintf("  judge %.2f [%s]", r.JudgeScore, r.JudgeVerdict)
			}
			fmt.Println(line)
			if !r.Pass {
				for _, f := range r.Failures {
					fmt.Printf("        - %s\n", f)
				}
			}
		}
		fmt.Printf("\n%d/%d passed  %.2fms total  $%.4f\n", sum.Passed, sum.Scenarios, float64(sum.TotalDurationMS), sum.TotalCostCents/100)
		if len(regs) > 0 {
			fmt.Printf("\ndrift vs %s:\n", baselinePath)
			for _, r := range regs {
				if r.New {
					fmt.Printf("  + %-30s new scenario (pass=%v)\n", r.Name, r.NowPass)
					continue
				}
				line := fmt.Sprintf("  %s %-30s was %s now %s", "=", r.Name, markOf(r.WasPass), markOf(r.NowPass))
				if r.ScoreDelta != 0 {
					line += fmt.Sprintf("  judge %+.2f", r.ScoreDelta)
				}
				if r.Note != "" {
					line += "  <-- " + r.Note
				}
				fmt.Println(line)
			}
		}
	}

	// Drift webhook: best-effort POST of the report to the configured
	// URL (alerting: PagerDuty, Slack, a Lambda, ...).
	if *webhook != "" {
		data, err := json.Marshal(out)
		if err == nil {
			if err := postJSON(*webhook, data, 10*time.Second); err != nil {
				fmt.Fprintf(os.Stderr, "replay: webhook failed: %v\n", err)
			}
		}
	}

	// The gate applies in both output modes.
	if sum.Failed > 0 {
		return fmt.Errorf("%d scenario(s) failed against %s", sum.Failed, *endpoint)
	}
	if n := eval.CountRegressions(regs); n > 0 {
		return fmt.Errorf("%d regression(s) vs baseline %s", n, baselinePath)
	}
	return nil
}

// postJSON sends a JSON payload to a URL (webhook delivery).
func postJSON(url string, data []byte, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// mcp-serve
// ---------------------------------------------------------------------------

func cmdMCPServe(args []string) error {
	fs := flag.NewFlagSet("mcp-serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json")
	name := fs.String("name", "ernest", "server name reported to clients")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	rt, err := cfg.Build(nil)
	if err != nil {
		return err
	}
	defer rt.Close()

	srv := mcp.NewServer(rt.Agents, mcp.ServerOptions{Name: *name, Version: version})
	fmt.Fprintln(os.Stderr, "ernest mcp-serve: agents as tools:", agentNames(rt.Agents))
	return srv.ServeStdio(context.Background(), os.Stdin, os.Stdout)
}

// ---------------------------------------------------------------------------
// playground
// ---------------------------------------------------------------------------

func cmdPlayground(args []string) error {
	fs := flag.NewFlagSet("playground", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json")
	host := fs.String("host", "127.0.0.1", "listen host")
	port := fs.String("port", "9090", "listen port")
	static := fs.String("static", "", "built UI directory (optional)")
	demo := fs.Bool("demo", false, "boot a self-contained demo (mock agent, no config needed)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var rt *config.Runtime
	var failuresPath string
	if *demo {
		// Self-contained demo: mock agent + in-memory store, no keys.
		temp := 0.7
		demoCfg := &config.Config{
			Agents: []config.AgentConfig{
				{
					Name:         "demo",
					Description:  "A self-contained demo agent (mock provider, no API keys)",
					Provider:     "mock",
					Model:        "mock-1",
					Instructions: "You are the ernest demo agent. Be brief, friendly and use tools when they help.",
					Tools:        []string{"calculator", "http_fetch", "now"},
					Memory:       true,
					Temperature:  &temp,
				},
			},
			Store: config.StoreConfig{Type: "memory"},
		}
		var err error
		rt, err = demoCfg.Build(nil)
		if err != nil {
			return err
		}
	} else {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		rt, err = cfg.Build(nil)
		if err != nil {
			return err
		}
		failuresPath = cfg.Failures
	}
	defer rt.Close()

	srv, err := server.New(server.Options{Agents: rt.Agents, Store: rt.Store, Static: *static, FailuresPath: failuresPath})
	if err != nil {
		return err
	}
	defer srv.Close()

	addr := net.JoinHostPort(*host, *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	fmt.Printf("ernest playground: http://%s  (agents: %s)\n", ln.Addr(), agentNames(rt.Agents))
	fmt.Println("press ctrl-c to stop")
	server := &http.Server{Handler: srv.Handler()}
	err = server.Serve(ln)
	if err == http.ErrServerClosed || ctx.Err() != nil {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultFile, "path to ernest.json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ok := true
	check := func(name, status string, err error) {
		if err != nil {
			status = "FAIL " + err.Error()
			ok = false
		}
		fmt.Printf("  %-14s %s\n", name, status)
	}

	fmt.Println("ernest doctor")
	fmt.Printf("  %-14s %s %s/%s\n", "runtime", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		check("config", "", err)
	} else {
		check("config", fmt.Sprintf("OK %s (%d agents, %d mcp servers)", *cfgPath, len(cfg.Agents), len(cfg.MCPServers)), nil)
		for _, ac := range cfg.Agents {
			if strings.EqualFold(ac.Provider, "mock") {
				check("agent "+ac.Name, "OK mock provider (no key needed)", nil)
				continue
			}
			if strings.EqualFold(ac.Provider, "ollama") {
				check("agent "+ac.Name, "OK ollama (no key needed)", nil)
				continue
			}
			keyEnv := ac.APIKeyEnv
			if keyEnv == "" {
				keyEnv = defaultKeyEnvForDoctor(ac.Provider)
			}
			if keyEnv == "" || os.Getenv(keyEnv) == "" {
				check("agent "+ac.Name, "WARN "+ac.Provider+": "+keyEnv+" not set", fmt.Errorf("missing %s", keyEnv))
				continue
			}
			check("agent "+ac.Name, "OK "+ac.Provider+" ("+keyEnv+" set)", nil)
		}
		for _, mc := range cfg.MCPServers {
			if mc.Transport == "http" {
				check("mcp "+mc.Name, "OK http "+mc.URL, nil)
				continue
			}
			if _, err := exec.LookPath(mc.Command); err != nil {
				check("mcp "+mc.Name, "", fmt.Errorf("command %q not found on PATH", mc.Command))
				continue
			}
			check("mcp "+mc.Name, "OK command "+mc.Command, nil)
		}
		// The store must open (sqlite file writable).
		if strings.EqualFold(cfg.Store.Type, "sqlite") {
			dsn := cfg.Store.DSN
			if dsn == "" {
				dsn = "ernest.db"
			}
			st, err := storage.NewSQLiteStore(dsn)
			if err != nil {
				check("store", "", err)
			} else {
				check("store", "OK sqlite "+dsn, nil)
				_ = st.Close()
			}
		}
	}

	if !ok {
		return fmt.Errorf("doctor found issues")
	}
	fmt.Println("all checks passed")
	return nil
}

func defaultKeyEnvForDoctor(provider string) string {
	switch strings.ToLower(provider) {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	}
	return ""
}
