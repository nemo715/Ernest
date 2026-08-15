package main

// The knowledge template: a config-driven RAG agent. ernest.json wires
// an OpenRouter model (gpt-4o-mini) plus an embeddings block
// (text-embedding-3-small via OpenRouter's OpenAI-compatible endpoint);
// docs/ is the knowledge source directory, embedded and retrieved per
// run. main.go is the programmatic path (mock, keyless); use
// `ernest run` / `ernest playground` to exercise the RAG config.
//
// All string literals here are raw strings, so the scaffold content
// must not contain backticks (the quantum template has a generator for
// that case: scripts/gen-template-quantum.go).
func init() {
	newTemplates["knowledge"] = struct {
		summary string
		files   map[string]string
	}{
		summary: "RAG agent: knowledge sources + embeddings (OpenRouter), eval-ready",
		files: map[string]string{
			"go.mod":        scaffoldMod,
			"ernest.json":   knowledgeErnestJSON,
			"docs/guide.md": knowledgeDocGuide,
			"scenarios.json": knowledgeScenarios,
			".env.example":   knowledgeEnv,
			"README.md":      knowledgeReadme,
			"main.go":        knowledgeMain,
		},
	}
}

const knowledgeErnestJSON = `{
  "embeddings": {
    "provider": "compatible",
    "baseUrl": "https://openrouter.ai/api/v1",
    "model": "text-embedding-3-small",
    "apiKeyEnv": "OPENROUTER_API_KEY"
  },
  "agents": [
    {
      "name": "assistant",
      "description": "RAG assistant grounded in docs/",
      "provider": "compatible",
      "model": "openai/gpt-4o-mini",
      "baseUrl": "https://openrouter.ai/api/v1",
      "apiKeyEnv": "OPENROUTER_API_KEY",
      "instructions": "You are a precise assistant. Answer ONLY from the knowledge chunks you are given; if the answer is not in them, say so. Never invent facts.",
      "tools": ["calculator", "http_fetch", "now"],
      "memory": true,
      "knowledge": {
        "sources": ["docs"],
        "topK": 3
      }
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
`

const knowledgeDocGuide = `# Refund policy runbook

## Standard refunds

Customers may request a refund within 30 days of purchase. Standard
refunds are processed within 5 business days and returned to the
original payment method.

## Large refunds

Refunds over $500 require manager approval. The request must include
the order id, the reason, and a short justification from support.

## Promotional purchases

Items bought with a promo code are refundable only within 14 days and
only as store credit, unless local law says otherwise.

## Chargebacks

A chargeback is not a refund. When a customer opens a chargeback, pause
the account and escalate to the billing team within 24 hours.
`

const knowledgeEnv = `# Copy to .env and export the line, or set the var directly.
OPENROUTER_API_KEY=
`

const knowledgeScenarios = `{
  "scenarios": [
    {
      "name": "refund-500-threshold",
      "input": "A customer asks: can I get a refund on a $600 order?",
      "expect": {
        "status": "completed",
        "contextContains": ["Refunds over $500 require manager approval"],
        "outputContains": ["approval"]
      }
    },
    {
      "name": "chargeback-policy",
      "input": "What happens when a customer opens a chargeback?",
      "expect": {
        "status": "completed",
        "contextContains": ["A chargeback is not a refund"],
        "outputContains": ["billing team"]
      }
    }
  ]
}
`

const knowledgeReadme = `# knowledge template

A config-driven RAG agent: ernest.json wires an OpenRouter model
(gpt-4o-mini) plus embeddings (text-embedding-3-small, via OpenRouter's
OpenAI-compatible endpoint). The docs/ directory is the knowledge
source: it is embedded at startup and the top-k most relevant chunks are
injected into the system prompt on every run. The playground trace
shows exactly which chunks were retrieved (Context tab), and evals can
lock that in with expect.contextContains.

## Run (config-driven RAG)

1. Set your key: export OPENROUTER_API_KEY=sk-or-... (or set it in a
   .env file and export the line).

2. Run an agent turn:

     ernest run --input "Can I get a refund on a $600 order?"

   The retrieved chunks appear in the trace (ernest run prints them in
   JSON mode) and the model answers from docs/guide.md only.

3. Boot the playground to see the trace Context tab:

     ernest playground --port 9090

   Chat with the agent, then open the run detail page -> Context tab:
   the three retrieved chunks are listed verbatim.

4. Add more sources: drop .md or .txt files into docs/ (or list other
   files/directories in ernest.json under knowledge.sources).

## Eval

The suite asserts the runbook knowledge actually reaches the model:

     ernest eval --config ernest.json --scenarios scenarios.json --json --update-baseline

Then gate CI with --baseline eval-baseline.json. Provider embeddings
cost a fraction of a cent per ingestion; retrieval itself is local.

## main.go

The programmatic path (mock provider, no keys) — the same agent built
in code instead of from ernest.json. Real providers swap in with one
line, see the comment in main.go.
`

const knowledgeMain = `package main

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
)

// Programmatic agent demo (mock provider, no keys). The config-driven
// RAG setup lives in ernest.json (knowledge.sources + embeddings) and
// is used by the ernest run / ernest playground commands; this file
// shows the same agent built in code.
func main() {
	p := llm.NewMock(llm.MockConfig{Stream: true})
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant. Use tools when they help."
	a.Tools = core.BuiltinTools

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
`
