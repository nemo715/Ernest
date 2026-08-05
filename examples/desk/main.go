package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"ernest/internal/agent"
	"ernest/internal/config"
	"ernest/internal/core"
	"ernest/internal/team"
)

// The desk: a multi-agent team built from ernest.json. The lead decides
// when to delegate, using the delegate tool that team.New injects
// automatically. Providers come from ernest.json (swap mock -> a real
// provider and add your key to use a real model).
func main() {
	cfg, err := config.Load("ernest.json")
	must(err)
	rt, err := cfg.Build(nil)
	must(err)
	defer rt.Close()

	desk := team.New("desk", byName(rt.Agents, "lead"), byName(rt.Agents, "researcher"), byName(rt.Agents, "writer"))

	input := "Calculate 17*23 and 391/17. Report both results with the exact expressions, the current UTC date and time, and a one-line summary. Delegate the arithmetic to the researcher."
	if len(os.Args) > 1 {
		input = os.Args[1]
	}

	fmt.Printf("desk> %s\n\n", input)
	start := time.Now()

	ch, err := desk.Stream(context.Background(), input, team.RunOptions{})
	must(err)

	var (
		streaming   bool
		streamStart time.Time
		llmCalls    int
		toolCalls   int
		metrics     *core.RunMetrics
		completed   bool
	)
	for ev := range ch {
		switch ev.Type {
		case core.EventRunStart:
			fmt.Printf("· run %s started\n", ev.RunID)
		case core.EventMessageDelta:
			if !streaming {
				fmt.Print("· ")
				streaming = true
				streamStart = time.Now()
			}
			fmt.Print(ev.Delta)
		case core.EventMessageComplete:
			if streaming {
				fmt.Printf("  [%d ms]\n", time.Since(streamStart).Milliseconds())
			}
			streaming = false
		case core.EventDelegateStart:
			fmt.Printf("· delegate → %s: %s\n", ev.Agent, ev.Data)
		case core.EventDelegateEnd:
			fmt.Printf("· delegate ← %s done\n", ev.Agent)
		case core.EventToolCall:
			toolCalls++
			fmt.Printf("· tool %s → %s\n", ev.ToolCall.Name, ev.ToolCall.Arguments)
		case core.EventToolResult:
			fmt.Printf("· tool %s ← %s\n", ev.ToolResult.Name, ev.ToolResult.Content)
		case core.EventTraceSpan:
			if ev.Span.Kind == "llm" {
				llmCalls++
			}
			if ev.Span.Tokens != nil {
				fmt.Printf("· span %-16s %5d ms  %d+%d tok\n", ev.Span.Name, ev.Span.DurationMS, ev.Span.Tokens.InputTokens, ev.Span.Tokens.OutputTokens)
			}
		case core.EventRunMetrics:
			metrics = ev.Metrics
		case core.EventRunComplete:
			completed = true
			fmt.Printf("\n· run.complete: %s\n", ev.Result.Status)
		case core.EventRunError:
			fmt.Printf("\n· run.error: %s\n", ev.Result.Error)
		}
	}

	fmt.Printf("\n— desk summary —\n")
	fmt.Printf("elapsed: %s   llm calls: %d   tool calls: %d\n", time.Since(start).Round(time.Millisecond), llmCalls, toolCalls)
	if metrics != nil {
		fmt.Printf("iterations: %d   cost: $%.4f", metrics.Iterations, metrics.CostCents/100)
		if metrics.Tokens != nil {
			fmt.Printf("   tokens: %d in / %d out", metrics.Tokens.InputTokens, metrics.Tokens.OutputTokens)
		}
		fmt.Println()
	}
	if !completed {
		os.Exit(1)
	}
}

func byName(agents []*agent.Agent, name string) *agent.Agent {
	for _, a := range agents {
		if a.Name == name {
			return a
		}
	}
	must(fmt.Errorf("agent %q not found in ernest.json", name))
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		os.Exit(1)
	}
}
