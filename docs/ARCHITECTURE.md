# ernest Architecture

## Layer map

```
cmd/ernest                 CLI: init, new, run, playground, doctor, eval, mcp-serve
   │
internal/config            ernest.json → Runtime{Agents, Store, MCP clients}
   │
internal/server            HTTP: SSE /api/chat, WS /ws/chat, sessions, approvals,
   │                        traces, audit, A2A routes, static playground UI
   │
   ├─ internal/agent       One agent: tool loop, guardrails, approvals, memory
   ├─ internal/team        Multi-agent delegation (leader → members)
   ├─ internal/workflow    Sequential step orchestration
   ├─ internal/eval        Scenario runner for regression tests
   │
   ├─ internal/llm         Providers: mock, openai, compatible, anthropic, gemini
   ├─ internal/mcp         MCP client (stdio/http) + MCP server mode
   ├─ internal/a2a         A2A server: agent cards, JSON-RPC dispatch
   ├─ internal/browser     Headless Chrome tool (CDP, lazy-launch)
   │
   ├─ internal/core        Wire format: RunEvent, RunResult, Tool, schema helpers
   ├─ internal/storage     Session store: in-memory, sqlite (+ postgres backend)
   ├─ internal/vector      Embedding store: in-memory, qdrant
   ├─ internal/knowledge   Knowledge base ingestion/retrieval
   ├─ internal/memory      Per-session conversation memory
   └─ internal/audit       Append-only audit log

web/                       Next.js playground (WS transport, SSE fallback)
python/ernest              Sync + async SDK mirroring the HTTP API
```

## The wire format is the contract

Every transport — SSE, WebSocket, MCP server mode, Python SDK — speaks the same
stream of `RunEvent` objects (camelCase JSON), defined once in
`internal/core/events.go`.

Event fields (all optional; the payload's `type` tells you what is set):

| Field | Carries |
|---|---|
| `type` | `run.start`, `message.delta`, `message.complete`, `tool.call`, `tool.result`, `approval.requested`, `approval.resolved`, `step.start`, `step.end`, `delegate.start`, `delegate.end`, `trace.span`, `run.metrics`, `run.complete`, `run.error` |
| `runId` | Run identifier (correlates all events of one run) |
| `agent` | Agent name (for delegated events: the member agent) |
| `delta` | Token chunk for `message.delta` |
| `message` | Final message for `message.complete` |
| `toolCall` | Name + JSON arguments |
| `toolResult` | Name + `content` (raw JSON) — note: field is `Content`, not `Output` |
| `approval` | Approval id + the tool call awaiting decision |
| `step` | Step info (workflow) |
| `span` | `trace.span`: name, duration ms, token usage |
| `metrics` | `run.metrics`: iterations, cost, tokens |
| `result` | Final `RunResult` on `run.complete` |
| `error` | Failure detail on `run.error` |
| `data` | Nested payload — e.g. a member agent's full event stream inside `delegate.start/end` |

### One run, as the client sees it

```
run.start
  message.delta ×N                    ← streaming the model's answer
  tool.call    {calculator, args}     ← model asks for a tool
  tool.result  {calculator, content}  ← tool executes
  approval.requested                  ← only when a tool requires HITL
  approval.resolved                   ← after approve/deny (denied tools never run)
  trace.span   ×N                     ← internal timing/token spans
  delegate.start                      ← leader hands off to a member
    ... member events in `data` ...
  delegate.end
  run.metrics  {iterations, costCents, tokens}
run.complete  {result}
```

## Runtime (agent loop)

1. **Redact** user input (regex patterns) and check budget caps
   (tokens/cost) — `internal/agent/agent.go`
2. Call the provider with the conversation + tool schemas
3. **Stream** deltas to the channel (`internal/agent/run.go`)
4. If the model emits a tool call:
   - deny-list check → blocked, never executes
   - require-approval list → emit `approval.requested`, pause until decision
   - else execute immediately
5. Append the tool result, repeat up to `maxIterations`
6. Emit `run.metrics` + `run.complete`

Hooks (`OnStart`, `OnMessage`, `OnToolCall`, `OnToolResult`, `OnFinish`) give
embedders a synchronous side-channel without parsing the event stream.

## Providers

`internal/llm/provider.go` defines the interface; implementations:

- `mock` — scripted/deterministic responses; zero network; used by tests,
  demos, and evals
- `openai` / `compatible` — OpenAI chat completions (SSE), any OpenAI-
  compatible endpoint (OpenRouter, Groq, Ollama, vLLM…) via `baseUrl`
- `anthropic`, `gemini` — native message formats

`config.Build` wires providers from `ernest.json`; keys come from the
environment via `apiKeyEnv` (ernest never loads `.env` itself).

## Storage & memory

- `internal/storage` — `memory` (default) and `sqlite` (modernc.org, pure Go,
  no CGO). A PostgreSQL backend (pgx) exists in `storage/postgres.go` but is
  not yet accepted by config validation. Sessions store message history per
  agent; `agent.Memory` persists them across runs.
- `internal/vector` — in-memory embedding store; Qdrant backend included.
- `internal/knowledge` — knowledge-base ingestion and retrieval on top of
  the vector store.

## Interop

- **MCP client** (`internal/mcp`) — attach external tools per agent via
  `mcpServers` in `ernest.json` (stdio or HTTP transports).
- **MCP server mode** (`ernest mcp-serve`) — the inverse: every configured
  agent becomes a tool callable from any MCP client.
- **A2A server** (`internal/a2a`) — agent cards + JSON-RPC dispatch at
  `/a2a/{agent}`, discovery document at `/.well-known/agent.json`. Team
  delegation reuses the A2A call path.

## Browser tool

`internal/browser` wraps go-rod (CDP). It is deliberately **not** in the
core built-in registry — the heavy dependency only loads when an agent opts in
via `"tools": ["browser"]`, and the Chrome process launches lazily on first
use.

## Web UI & SDKs

- `web/` — Next.js playground. Connects over WebSocket (`/ws/chat`, transport
  id `ErnestWS`), falls back to SSE, renders live deltas/tools/approvals/
  metrics, and supports interrupt/steer mid-run.
- `python/ernest` — stdlib-only SDK. `ErnestClient` (sync) and
  `AsyncErnestClient` mirror the HTTP API: `stream_chat`, `chat`,
  `stream_approve`, `approve`, `list_agents`, `list_sessions`,
  `get_session`, `delete_session`, `get_run_trace`, `health`. Event constants
  use `EVENT_*` naming; `Session.messages` and `SessionInfo.messages` are the
  message fields.

## Evals

`internal/eval` loads scenarios (single-line JSON objects or a `scenarios`
array; files or directories — `ernest.json` is skipped) and runs them through
a real agent, asserting on status, tool-call counts, substrings, and outputs.
`ernest eval` is a thin CLI wrapper, designed for CI.

## Benchmarks

`bench/main.go` measures framework overhead with the mock provider (raw call,
agent chat, agent + tool, streaming, heap) and — with `-real` and
`OPENROUTER_API_KEY` — real-model latency and time-to-first-token.
`bench/lc_bench.py` / `bench/lc_real.py` are the langchain-core counterparts.
Results and honest interpretation live in [TEST.md](../TEST.md).
