// Command bench measures ernest's framework overhead with the
// deterministic mock provider (no network, no API keys):
//
//   - raw:        direct provider call (the model-layer baseline)
//   - agent:      full agent turn (events, memory wiring, tool layer)
//   - agent+tool: agent turn that executes the calculator tool
//   - stream:     agent streaming turn, consuming all events
//
// Run: go run ./bench            (mock overhead, no network)
//      go run ./bench -real      (real model via OpenRouter, needs OPENROUTER_API_KEY)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

var real = flag.Bool("real", false, "benchmark against a real model (OpenRouter, requires OPENROUTER_API_KEY)")

const (
	warmup = 100
	iters  = 2000
)

func main() {
	flag.Parse()
	ctx := context.Background()
	if *real {
		runReal(ctx)
		return
	}
	// Raw provider baseline: one mock model call.
	p := llm.NewMock(llm.MockConfig{})
	req := llm.ChatRequest{Messages: []core.Message{core.NewUserMessage("Say hello")}}
	run(ctx, "raw provider (model call only)", func() {
		_, err := p.Chat(ctx, req)
		if err != nil {
			panic(err)
		}
	})

	// Full agent turn: message assembly, event loop, result building.
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant."
	a.Tools = core.BuiltinTools
	run(ctx, "ernest agent.Chat (full turn)", func() {
		_, err := a.Chat(ctx, "Say hello", agent.RunOptions{})
		if err != nil {
			panic(err)
		}
	})

	// Agent turn that actually executes the calculator tool (2 model calls).
	pt := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"6*7"}`)}}, FinishReason: "tool_calls"},
		{Content: "42", FinishReason: "stop"},
	}})
	at := agent.New("assistant", pt)
	at.Tools = []*core.Tool{core.Calculator}
	run(ctx, "ernest agent.Chat + tool execution (2 calls)", func() {
		_, err := at.Chat(ctx, "What is 6*7?", agent.RunOptions{})
		if err != nil {
			panic(err)
		}
	})

	// Streaming turn: consume every event from the channel.
	ps := llm.NewMock(llm.MockConfig{Stream: true, Script: []llm.MockTurn{
		{Content: "Hello streaming world", FinishReason: "stop"},
	}})
	as := agent.New("assistant", ps)
	run(ctx, "ernest agent.Stream (event loop)", func() {
		ch, err := as.Stream(ctx, "Say hello", agent.RunOptions{})
		if err != nil {
			panic(err)
		}
		for range ch {
		}
	})

	// Memory footprint after warmup.
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	fmt.Printf("\nprocess heap after warmup: %d KiB\n", m.HeapAlloc/1024)
}

func runReal(ctx context.Context) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENROUTER_API_KEY is not set")
		os.Exit(1)
	}
	p := llm.NewOpenAICompat(llm.OpenAICompatConfig{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  key,
		Model:   "openai/gpt-4o-mini",
	})

	const prompt = "Reply with the single word: ok"
	agentFor := func() *agent.Agent {
		a := agent.New("assistant", p)
		a.Instructions = "You are a helpful assistant."
		return a
	}
	realRun(ctx, "ernest agent.Chat (real, 20 turns)", func() (*core.RunResult, error) {
		return agentFor().Chat(ctx, prompt, agent.RunOptions{})
	}, 1, 20)

	realTTFB(ctx, "ernest agent.Stream first-token (real, 5 turns)", func() (<-chan core.RunEvent, error) {
		return agentFor().Stream(ctx, prompt, agent.RunOptions{})
	}, 5)

	realRun(ctx, "ernest agent.Chat + calculator (real, 5 turns)", func() (*core.RunResult, error) {
		a := agentFor()
		a.Tools = []*core.Tool{core.Calculator}
		return a.Chat(ctx, "What is 17*23?", agent.RunOptions{})
	}, 1, 5)
}

// realRun times N sequential agent turns against a real model and prints
// ms percentiles plus estimated token cost (gpt-4o-mini list pricing).
func realRun(ctx context.Context, name string, fn func() (*core.RunResult, error), warm, n int) {
	var times []time.Duration
	var inT, outT int
	for i := 0; i < warm+n; i++ {
		t0 := time.Now()
		r, err := fn()
		d := time.Since(t0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: turn %d failed: %v\n", name, i+1, err)
			continue
		}
		if i >= warm {
			times = append(times, d)
		}
		if r.Usage != nil {
			inT += r.Usage.InputTokens
			outT += r.Usage.OutputTokens
		}
	}
	if len(times) == 0 {
		return
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	stats := func(p float64) float64 { return float64(times[int(p*float64(len(times)-1))]) / 1e6 }
	var sum float64
	for _, d := range times {
		sum += float64(d) / 1e6
	}
	fmt.Printf("%s: mean %6.1f ms  p50 %6.1f ms  p95 %6.1f ms  tokens %d+%d  est $%.4f\n",
		name, sum/float64(len(times)), stats(0.50), stats(0.95), inT, outT,
		(float64(inT)*0.15+float64(outT)*0.60)/1e6)
}

// realTTFB measures time-to-first-content-event for streamed turns.
func realTTFB(ctx context.Context, name string, fn func() (<-chan core.RunEvent, error), n int) {
	var ttfb []time.Duration
	for i := 0; i < n; i++ {
		ch, err := fn()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			return
		}
		t0 := time.Now()
		for ev := range ch {
			if ev.Type == core.EventMessageDelta || ev.Type == core.EventMessageComplete {
				ttfb = append(ttfb, time.Since(t0))
				break
			}
		}
		for range ch {
		}
	}
	var sum float64
	for _, d := range ttfb {
		sum += float64(d) / 1e6
	}
	fmt.Printf("%s: mean %6.1f ms first token\n", name, sum/float64(len(ttfb)))
}

func run(ctx context.Context, name string, fn func()) {
	for i := 0; i < warmup; i++ {
		fn()
	}
	times := make([]time.Duration, iters)
	start := time.Now()
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		fn()
		times[i] = time.Since(t0)
	}
	elapsed := time.Since(start)
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	pct := func(p float64) time.Duration { return times[int(p*float64(iters-1))] }
	fmt.Printf("%-44s mean %8.1f µs   p50 %7.1f µs   p95 %7.1f µs   p99 %7.1f µs   %6.0f turns/s\n",
		name, float64(elapsed.Microseconds())/iters, float64(pct(0.50).Microseconds()),
		float64(pct(0.95).Microseconds()), float64(pct(0.99).Microseconds()), float64(iters)/elapsed.Seconds())
}
