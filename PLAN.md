# ernest — plan & architecture

A fast multi-agent framework in Go, with a Next.js/TypeScript playground UI
and a Python SDK. The core is a single static Go binary; the UI is a static
export served by the binary.

## Repository layout

```
cmd/ernest/            CLI: init, run, playground (--demo), doctor, new, eval, mcp-serve
internal/core/         types, errors, events (wire format), JSON schema engine
internal/llm/          providers: OpenAI-compat, Anthropic, Gemini, Mock (+ SSE)
internal/agent/        agent runner: chat/stream, tools, memory, HITL, hooks, guardrails
internal/tools/        (tool layer inside core: schema.go, tool.go, expr.go)
internal/memory/       session memory + trim strategies
internal/storage/      session stores: in-memory, SQLite, Postgres
internal/knowledge/    knowledge base (vector-backed retrieval)
internal/vector/       vector stores: in-memory, Qdrant
internal/workflow/     workflow engine (steps, events)
internal/team/         multi-agent team orchestration
internal/mcp/          MCP clients: HTTP (SSE) + stdio
internal/a2a/          A2A agent cards + JSON-RPC server + client tool
internal/browser/      browser tool (rod/Edge), lazy CDP
internal/eval/         scenario evals (asserts, samples, iterations)
internal/server/       HTTP API + SSE streaming + /ws/chat transport, trace/audit endpoints
web/                   Next.js/TS playground UI (static export → out/)
web/scripts/           dev tools: mock backend, static file server
```

## Status

| Module | Code | Tests |
|---|---|---|
| core (types/events/schema/expr/tools) | done | done |
| llm providers (openai/anthropic/gemini/mock/sse) | done | done |
| agent runner (chat/stream/tools/memory/knowledge/HITL) | done | done |
| guardrails (input/output/depth/time) | done | done |
| tracing (trace.span / run.metrics / /api/runs/{id}/trace) | done | done |
| context engineering (run context: system prompt + knowledge + history; `contextContains` evals; /api/runs list) | done (v0.1.7) | done |
| failures feed (`failures.jsonl` + /api/failures + console page) | done (v0.1.7) | done |
| storage (mem/sqlite/postgres) + vector + knowledge | done | done |
| workflow + team | done | done |
| mcp (http + stdio) | done | done |
| mcp completeness (client resources/prompts; server streamable-HTTP `--http`) | done (v0.1.7) | done |
| a2a (agent cards + JSON-RPC + client tool) | done | done |
| browser tool (rod/Edge) | done | done |
| eval harness (`ernest eval`) | done | done |
| CLI (init/run/playground/doctor/new/eval/mcp-serve) | done | CLI flows E2E |
| HTTP/SSE server + /ws/chat transport | done | done (ws tests) |
| web UI (streaming + HITL + ws transport + interrupt/steer) | done | build-verified |
| dev console (runs/traces/context, sessions, approvals, failures, audit; bulbul logo; SPA fallback) | done (v0.1.7) | build-verified |
| Python SDK (sync + async, trace fetch) | done | done (59 pytest) |
| examples (Go + Python SDK) | done | python examples E2E-verified |

## Wire format

Single source of truth: `internal/core/events.go` + `types.go`.
Consumed by: CLI (`--json`), the web UI (`web/lib/types.ts`), and any HTTP
client. Do not rename JSON tags without updating all three.

Event types: `run.start`, `message.delta`, `message.complete`, `tool.call`,
`tool.result`, `approval.requested`, `approval.resolved`, `step.start`,
`step.end`, `delegate.start`, `delegate.end`, `run.complete`, `run.error`,
`trace.span`, `run.metrics`.

WebSocket protocol (`GET /ws/chat`): client sends `chat|steer|interrupt|
approve|ping`; server replies `ready|pong|ack|error` + the same RunEvent
frames. One active run per connection; a `steer` cancels the current
generation and queues a follow-up run on the same session.

## Web UI

- `web/` — Next.js 15 (App Router) + TypeScript, `output: "export"`.
- Build: `npm run build` → `web/out/`; serve with `ernest playground --static web/out`.
- Dev: `npm run dev` (client auto-falls back to `http://127.0.0.1:9090`;
  override with `NEXT_PUBLIC_ERNEST_API_URL` at build time).
- Mock backend without Go: `npm run mock` (replicates the SSE contract).
- Static host for the export: `npm run build && node scripts/serve-static.mjs`.
- Transport: the playground connects to `/ws/chat` via `ErnestWS`
  (`web/lib/api.ts`), falling back to SSE (`/api/chat`) when the socket
  can't be opened; the top bar shows a `ws`/`sse`/`connecting…` pill.
  While streaming on WS: Interrupt sends `{type:"interrupt"}` and the
  run unwinds with `run.error`; the “Redirect the run…” box sends
  `{type:"steer"}` which cancels the current generation and starts a
  follow-up run on the same session.

## CLI

- `ernest new <agent|team|workflow|server> [dir]` — writes a valid
  `ernest.json` (mock provider, no keys) + a compiling `main.go` using
  `internal/agent`, `internal/team`, `internal/workflow` or
  `internal/server`; no args prints the template list.
- `ernest playground --demo` — boots a self-contained server with a mock
  “demo” agent and an in-memory store; no `ernest.json` required.
- `ernest eval <dir>` — scenario evals (asserts, samples, iterations).
- `ernest mcp-serve` — exposes agents as MCP tools (stdio or SSE).

## Examples

- `examples/go/chat.go` — agent + tool + HITL approval with the mock
  provider; `go run ./examples/go/chat.go`. Verified: streams tokens,
  pauses for approval, resumes, completes with usage.
- `examples/python/chat.py` — sync SDK: stream, approve, sessions;
  `python examples/python/chat.py` (works with `npm run mock` on :9090).
- `examples/python/stream_async.py` — async SDK streaming + approve.
- `examples/python/errors.py` — typed error handling (APIError / RunError).

All three Python examples are E2E-verified against the mock backend
(exit 0); each embeds a small `sys.path` bootstrap so it runs in-place
without installing the SDK. The SDK is also verified against the real
ernest server (`ernest playground` + mock agent + sqlite store):
agents/health, chat streams (`run.start` → `message.complete` →
`run.complete`), sessions CRUD — all green.

## Backend resolution in the UI client

`web/lib/api.ts` tries same-origin first, then `http://127.0.0.1:9090`.
A non-JSON 404 (or network error) means "not the ernest API" → next base.

## Python SDK

- `python/` — zero-dependency SDK (stdlib only): sync `Client` + async
  `AsyncClient` for `/api/chat` + `/api/approve` (SSE streaming),
  `/api/sessions`, `/api/agents`, `/healthz`, and `get_run_trace(run_id)`
  (`GET /api/runs/{id}/trace` → `RunTrace{run_id, spans, metrics}`).
- `ernest/types.py` mirrors the wire format (camelCase JSON → snake_case
  attrs) including `TraceSpan` / `RunMetrics` and the `trace.span` /
  `run.metrics` events; `ernest/errors.py` maps `run.error` kinds
  (`agent_error`, `provider_error`, `tool_error`, …) to typed exceptions.
- Usage: `from ernest import Client; Client().chat("assistant", "hi")`
- Tests: `cd python && python -m pytest -q` (mock SSE server in
  `python/tests/conftest.py` replicates the contract incl. a `trace-me`
  flow and a trace store; 59 tests).

## Final verification (Go 1.26.5 portable, no admin)

- Toolchain: go.dev zip → `%LOCALAPPDATA%\Programs\go` (extract with
  `tar.exe -xf`, not `Expand-Archive` — much faster; `Set-Content
  -Encoding utf8` writes a BOM that Go JSON parsing rejects).
- `go vet ./...` clean after fixing: `internal/config` `keyEnv`
  assignment mismatch, `internal/team` copylocks (Agent holds a Mutex).
- `go test ./...` — all 18 packages pass (a2a, agent, browser, core,
  eval, knowledge, llm, mcp, server, storage, team, vector, workflow, …).
- `StreamResume` now fails fast (unknown approval / no store) instead of
  returning a stream that closes silently; regression tests added.
- CLI: `go build ./cmd/ernest` → `doctor`, `playground --demo`, `new`
  (all four scaffolds compile), `eval`, `mcp-serve` work; the playground
  + Python SDK round-trip (chat SSE, sessions, trace fetch) is verified
  above, and the live `--demo` server emits `trace.span` + `run.metrics`
  with a working `/api/runs/{id}/trace`.
