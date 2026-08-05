# ernest — testing

## Go core (requires Go toolchain)

All core behavior lives in `internal/` with table/mock-based unit tests —
no external services needed.

```
go test ./...    # verified green on go1.26.5 (windows/amd64)
```

| Package | Covers |
|---|---|
| `internal/core` | types, errors, events, JSON schema engine, expression engine, tool layer |
| `internal/llm` | SSE transport, OpenAI-compat, Anthropic, Gemini, Mock providers |
| `internal/agent` | chat/stream, tool calls, memory, knowledge, HITL approvals, hooks |
| `internal/storage` | in-memory / SQLite / Postgres session stores |
| `internal/vector` | vector stores (in-memory, Qdrant adapter) |
| `internal/knowledge` | knowledge base retrieval |
| `internal/workflow` | workflow engine (steps, events) |
| `internal/team` | multi-agent orchestration |
| `internal/mcp` | MCP client: HTTP (SSE) + stdio (mocked servers) |
| `internal/a2a` | A2A agent cards + JSON-RPC server + client tool |
| `internal/browser` | browser tool (rod/Edge), lazy CDP |
| `internal/eval` | scenario evals (asserts, samples, iterations) |
| `internal/server` | HTTP API, SSE streaming, /ws/chat transport, trace + audit endpoints |

Note: `cmd/ernest` is covered by the CLI flows below.

Verified state (portable go1.26.5, no admin):

- `go vet ./...` clean — after fixing `internal/config` (keyEnv
  assignment mismatch) and `internal/team` (copylocks: Agent holds a
  `sync.Mutex`, so the leader is rebuilt as a fresh struct).
- `go test ./...` — all 18 packages pass.
- `go build ./cmd/ernest` — CLI works (`doctor`, `playground`, `new`,
  `eval`, `mcp-serve`).
- `ernest new <agent|team|workflow|server>` — all four scaffolds write a
  valid ernest.json + a compiling main.go (verified with `go build ./...`).
- `ernest playground --demo` — boots a self-contained mock-agent server
  with no ernest.json; SSE stream emits trace.span + run.metrics, and
  `/api/runs/{id}/trace` returns the spans (verified via curl).
- `go run ./examples/go/chat.go` — HITL flow verified: tokens →
  tool.call → approval.requested → resume → approval.resolved →
  tool.result → run.complete(completed, usage 412/96).
- Real-server round-trip: `ernest playground` (mock agent + sqlite
  store) + Python SDK — agents/health, chat SSE (`run.start` →
  `message.complete` → `run.complete`), sessions CRUD, run trace fetch
  all green.
- `/ws/chat` transport is covered by `internal/server` tests (dial,
  chat events, interrupt ack, steer follow-up, approve resume).

Fixed in this pass: `StreamResume` previously swallowed validation
errors in its goroutine, returning a stream that closed without events
(e.g. resuming an ephemeral run with no Memory/Store). It now fails fast
with a clear error; regression tests: `TestStreamResumeUnknownApproval`,
`TestStreamResumeWithoutStoreFailsFast`.

Also fixed: the mock provider answered with an *empty* reply when no
script/default turn was configured — so `ernest new` scaffolds, `ernest
run` and `playground --demo` all streamed blank messages. The unscripted
mock now returns a canned response (with usage tokens), so every
out-of-the-box flow shows output (`go test ./...` green after the
change).

Also fixed (found with a real provider): the OpenAI-compatible client
serialized follow-up `tool_calls[].function.arguments` as a JSON object
instead of the JSON-encoded STRING the OpenAI/OpenRouter wire format
requires, so real multi-iteration tool runs died with HTTP 400 on the
second model call (the mock provider never noticed). Response-side
arguments are now unquoted raw JSON for the tool layer. Regression
`TestOpenAICompatToolCallRoundTrip`; the existing chat test now asserts
unquoted arguments too.

Real-provider verification (OpenRouter, `compatible` provider,
`openai/gpt-4o-mini`): `ernest doctor` OK with key; `ernest run` one-shot
answered; playground SSE ran a full tool-calling turn — real model
issued `calculator` + `now` calls, tools executed (391 / UTC time), the
final answer streamed as `message.delta`s, spans recorded real token
counts (219/43, 517/84), `run.complete(completed, 2 iterations)`.

## Benchmarks

Framework overhead with deterministic mock/fake LLMs (no network, no API
keys) — see `bench/` (`go run ./bench`, `bench/venv/Scripts/python.exe
bench/lc_bench.py`). Windows, go1.26.5, Python 3.13, langchain-core 1.5.3.

| Metric | ernest | langchain-core | Comparable? |
|---|---|---|---|
| raw model-layer call | 0.3 µs | 183 µs | No — different work and measurement floors (see below) |
| full agent turn vs bare chain invoke | 17.6 µs (events + tool layer) | 494 µs (prompt\|llm) | Partial — different work; indicative, not precise |
| streaming turn (event loop) | 50.3 µs | n/a (not measured) | — |
| process footprint | 370 KiB heap | 817 modules loaded | Both real, not the same unit |
| deploy size | 20.4 MB single binary (server+UI+CLI) | 42 MB site-packages (+ python runtime, usually preinstalled) | Yes (both are “framework” footprint) |
| cold start | 17–19 ms process boot | 235 ms library import | No — process boot vs import are different steps |

Honest interpretation:

- The raw-call numbers are **not a valid head-to-head**: ernest's mock is a
  trivial struct return measured near the timer floor; FakeListChatModel
  does pydantic-validated message conversion. The 600x figure previously
  here was misleading and is withdrawn.
- The agent-vs-chain numbers both measure “work beyond the model”, but
  the work is not equal, the languages differ, and pydantic validation is
  included on one side only. Directionally ernest is faster; the 28x is
  indicative, not a precise ratio.
- **With a real model, the framework tax is noise per turn**: at ~300 ms
  model latency, ernest adds ~0.02 ms and langchain-core ~0.5 ms — a
  ~0.2% difference. The tax matters only where turns run unattended or at
  scale: batch evals, high-concurrency serving, serverless cold starts,
  memory-constrained environments.
- Unmeasured, so unknown: full agent loops on both sides (langgraph /
  CreateAgent with tools), real end-to-end server throughput under
  concurrent load, langchain process memory, anything with a real model.
- Where langchain-core wins (not measured, because it's not a number):
  ecosystem (thousands of integrations), Python as the default language
  for AI teams, documentation/community maturity, enterprise adoption.
  A benchmark cannot capture those; treat the tables above as
  performance-only evidence.

### Real-model run (OpenRouter, `openai/gpt-4o-mini`)

Same model, same endpoint, same prompt ("Reply with the single word: ok"),
sequential, back to back. ernest: `go run ./bench -real`; langchain:
`bench/venv/Scripts/python.exe bench/lc_real.py` (ChatOpenAI on
OpenRouter). Total cost of both runs: well under a cent.

| Metric | ernest | langchain-core (+openai) |
|---|---|---|
| per-turn, 20 turns (2 runs) | mean 0.86 s / 1.13 s, p50 ~1.0 s | mean 0.94 s, p50 0.93 s |
| streaming time-to-first-token (5 turns) | 0.91–1.00 s | 0.97 s |
| turn with calculator tool (2 model calls) | ~2.0 s | not measured (no agent loop in core) |

Interpretation: **parity within network noise** — run-to-run variance
(±30%) exceeds any framework difference. With a real model the framework
tax is invisible per turn; ernest's mock-bench advantage only matters
where turns run unattended at scale (evals, batch, high concurrency).
TTFB ~1 s is model/network-bound, identical on both sides.

## Example apps

`examples/desk` — a multi-agent team scaffolded exactly like a user would:
`ernest new team examples/desk`, then two edits (swap `mock` for the real
provider in ernest.json; wire `main.go` through `config.Load/Build` and
stream the transcript). `ernest doctor -config examples\desk\ernest.json`
passes; live run on OpenRouter (gpt-4o-mini): lead delegated arithmetic to
the researcher (delegate.start/end), researcher ran the calculator tool
(17×23=391, 391÷17=23), `now` returned UTC time, the final answer streamed
token-by-token — 3 llm calls, 2 tool calls, 6.5 s, 792+107 tokens, $0.00.
`ernest run -config examples\desk\ernest.json -agent lead -input ...` also
works (pure config, no Go).

Also fixed: `TestA2ATaskLifecycle` was a spin-poll race — the loop could
exhaust its 100 iterations before the async task goroutine was ever
scheduled (reproduced 5/5, worker completed fine after the loop). Added
`time.Sleep(5ms)` between polls; passes 5/5 and in the full suite.

## Web UI

UI-only development needs no Go — the mock backend (`npm run mock`)
replicates the SSE contract on `127.0.0.1:9090`.

```
cd web
npm run build         # must exit 0; produces web/out/ (static export)
npm run dev           # :3000, hot reload
npm run mock          # :9090 mock backend (separate terminal)
node scripts/serve-static.mjs   # host the export on :8080
```

### Manual E2E checklist (against mock backend)

1. **Streaming** — send a message; tokens appear incrementally; run
   completes with usage tokens.
2. **Tool cards** — a `tool.call` shows a running card with args; the
   matching `tool.result` resolves it (result or error JSON).
3. **Approve flow** — approval card shows PENDING with args + note input;
   click Approve → card flips to APPROVED, tool card resolves, run
   completes; the note appears on the card.
4. **Reject flow** — click Reject → card flips to REJECTED, tool shows
   `tool call rejected by human`, agent replies "skipped".
5. **Interrupt** — during streaming (WS transport), the Interrupt button
   sends `{type:"interrupt"}`; the run unwinds with run.error
   (interrupted) and the UI settles. With the SSE fallback it aborts the
   fetch instead.
6. **Steer** — while streaming on WS, type in the “Redirect the run…”
   box and press Enter: the current generation is cancelled and a new
   run with the steer text starts on the same session.
7. **Transport pill** — the top bar shows `ws` (live socket), `sse`
   (fallback) or `connecting…`.
8. **Sessions** — sidebar lists sessions; new chat creates a session;
   resuming a session replays history; delete removes it.
9. **Agents** — dropdown populates from `/api/agents`.
10. **Cross-origin fallback** — open the static export (port 8080) without
   a same-origin API: requests fall back to `http://127.0.0.1:9090`
   (verify in the network tab).
11. **Mobile** — viewport <760px: sidebar hidden, agent select + new-chat
   visible in the top bar, chat usable.

### Known pitfalls (from E2E validation)

- Approval resume replays the blocked tool call with the **same call id**;
  the UI must look up tool cards across all assistant items
  (`findToolOwner`), not just the active one.
- If the resume stream dies, the UI must revert a resolving approval card
  back to PENDING (finally-block) so the user can retry.
- The static file server must 404 with `text/html` (non-JSON) — the client
  treats any non-JSON 404 as "not the ernest API" and falls back.

## Python SDK

Zero-dependency SDK (`python/`): sync `Client` + async `AsyncClient`.

```
cd python
python -m pytest -q          # 59 tests, no external services
```

Coverage:

| Area | Covers |
|---|---|
| `test_errors.py` | run.error → typed exceptions (all 10 kinds), HTTP status → APIError subclasses |
| `test_client.py` | sync chat/approve SSE streams, event ordering, HITL approve/reject, session CRUD, URL quoting, typed run errors, 400/404 mapping, approve-after-session-delete → 404, trace.span/run.metrics parsing, `get_run_trace` (+ tool spans, 404) |
| `test_async_client.py` | same flows on asyncio (no pytest plugins needed), early stream abort closes the connection cleanly, async trace fetch |

Mock backend: `python/tests/conftest.py` replicates the SSE contract
(same event ordering as `web/scripts/mock-server.mjs`) on a random
`127.0.0.1` port, including error-path hooks (`boom`/`tool-boom`/
`provider-boom` inputs → typed `run.error` events) and a `trace-me`
input that emits `trace.span` + `run.metrics`, plus a
`GET /api/runs/{id}/trace` store mirroring `server.traces`.

Pitfall learned: the mock must close the connection after the final SSE
frame (`close_connection = True`) — the real Go server ends the stream
when `streamEvents` returns, and clients read frames until EOF.

Also fixed: `DELETE /api/sessions/{id}` must clean the shared
approval→session map at the class level — assigning via `self.` created
an instance attribute that shadowed it, so deleted sessions' approvals
would still resolve (regression test: `test_approve_after_session_delete_raises`).

## Examples (E2E against the mock)

Run `npm --prefix web run mock` (backend on :9090), then from the repo root:

```
python examples/python/chat.py          # stream → approve → sessions, exit 0
python examples/python/stream_async.py  # async stream → approve, exit 0
python examples/python/errors.py        # 404 + error_from_event mapping, exit 0
go run ./examples/go/chat.go            # Go example (Go toolchain required)
```
