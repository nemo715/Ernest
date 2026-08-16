# ernest Architecture

## Layer map

```
agent/ core/ llm/ team/ workflow/ server/ storage/   public API: thin forwarding
   │      packages aliasing the internal types (module
   │      github.com/nemo715/Ernest — importable by any project)
   │
cmd/ernest                 CLI: init, new, run, playground, doctor, eval, replay, mcp-serve
   │
internal/config            ernest.json → Runtime{Agents, Teams, Workflows, Store, MCP clients}
   │                        toolSandbox/toolPolicy resolution, approval defaults
   │
internal/server            HTTP: SSE /api/chat, WS /ws/chat, sessions, approvals,
   │                        traces, audit, A2A routes, /api/teams + /api/workflows,
   │                        static playground UI
   │
   ├─ internal/agent       One agent: tool loop, guardrails, approvals, memory
   ├─ internal/team        Multi-agent delegation (leader → members) + sequential process
   ├─ internal/workflow    DAG step orchestration (prompts, guards, retries)
   ├─ internal/eval        Scenario runner for regression tests
   │
   ├─ internal/llm         Providers: mock, openai, compatible, anthropic, gemini
   ├─ internal/mcp         MCP client (stdio/http) + MCP server mode
   ├─ internal/a2a         A2A server: agent cards, JSON-RPC dispatch
   ├─ internal/browser     Browser pack tools (CDP, lazy-launch)
   │
   ├─ internal/core        Wire format: RunEvent, RunResult, Tool, schema helpers,
   │                        tool packs (files, shell, web_search) + sandbox guardrails
   ├─ internal/storage     Session store: in-memory, sqlite (+ postgres backend)
   ├─ internal/vector      Embedding store: in-memory, qdrant
   ├─ internal/knowledge   Knowledge base ingestion/retrieval
   ├─ internal/memory      Per-session conversation memory
   └─ internal/audit       Append-only audit log

web/                       Next.js console (WS transport, SSE fallback)
python/ernest              Sync + async SDK, authoring DSL (Agent/Task/Crew/Team),
                           `python -m ernest` CLI (compiles DSL → Go engine)
```

The public packages at the module root (`agent`, `core`, `llm`, `team`,
`workflow`, `server`, `storage`) are one-file forwarding packages that alias
(`type X = internal.X`) the implementation types. Go's `internal/` rule
forbids importing `ernest/internal/*` from outside the module, so the
forwarding packages are what `ernest init` / `ernest new` scaffolds use —
making scaffolded projects compilable anywhere.

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

## Teams & workflows (config-driven)

`internal/config` builds `team.Team` and `workflow.Workflow` objects from
the `teams` / `workflows` arrays of `ernest.json`:

- **Teams**: `process: "hierarchical"` (default) — a leader agent gets a
  built-in `delegate` tool and decides delegation itself;
  `"sequential"` — members run in declaration order with each output
  feeding the next (deterministic, no leader model call). Sequential
  runs emit `delegate.start/end` per member and carry
  `{team, process, members[]}` metadata.
- **Workflows**: a DAG of steps (`name`, `agent`, `prompt` with
  `{{input}}`/`{{step}}` placeholders, `dependsOn`, optional LLM-judged
  `guard` and `retries`). Independent steps run concurrently; each step
  streams `step.start`/`step.end`; the final state maps step names to
  outputs.

Both surfaces are exposed three ways: CLI (`ernest run --team/--workflow`),
HTTP (`GET /api/teams`, `GET /api/workflows`, `POST .../run` → SSE), and the
public Go packages (`team`, `workflow`). The Python DSL compiles to the same
config shapes (see [PYTHON.md](PYTHON.md)).

## Tool packs

`internal/core/toolpacks.go` implements the sandboxed packs:

- `file_read` / `file_list` / `file_write` — operate **only** inside the
  agent's `toolSandbox` directory (relative paths resolve inside it;
  absolute paths and `..` escapes are rejected). `file_read` caps
  output at 32 KB.
- `shell_exec` — runs inside the sandbox (`cmd /C` on Windows,
  `sh -c` elsewhere), 30 s default timeout (300 s max), exit code +
  16 KB-capped output. Gated by `toolPolicy.enableShell` (off by
  default), **always** requires approval — a deny structurally prevents
  execution (the approval gate runs before any subprocess starts).
- `web_search` — DuckDuckGo HTML endpoint (no API key), result/snippet
  extraction with redirect decoding; `ERNEST_WEB_SEARCH_URL` overrides
  the endpoint for tests.

Policies are resolved at build time in `internal/config`: a default
approval set (`file_write` + browser pack tools), minus `autoApprove`
exemptions (never `shell_exec`), plus extra `requireApproval` tools.
The sandbox and approval sets land on the agent (`ToolSandbox`,
`RequireApprovalTools`) and are enforced in the run loop
(`internal/agent/run.go`), not at the tool-call site.

## Browser tools

`internal/browser` wraps go-rod (CDP). The heavy dependency only loads
when an agent opts in via `"tools"`, and the Edge/Chrome process launches
lazily on first use. The browser pack exposes five single-purpose tools —
`browser_navigate`, `browser_read`, `browser_click`, `browser_type`,
`browser_screenshot` — plus the legacy `browser` enum tool. Pack tools
validate arguments **before** launching the browser.

## Web UI & SDKs

- `web/` — Next.js console. Connects over WebSocket (`/ws/chat`, transport
  id `ErnestWS`), falls back to SSE, renders live deltas/tools/approvals/
  metrics, and supports interrupt/steer mid-run. Routes: overview,
  playground, approvals, runs & traces, sessions, failures, audit, agents,
  docs.
- `python/ernest` — stdlib-only. The **authoring DSL** (`Agent`, `Task`,
  `Crew`, `Team`, `Guard` in `dsl.py`) compiles to `ernest.json` and runs
  on the Go engine via `python -m ernest run/doctor` (`__main__.py`
  detects the module-level crew/team/workflow object; `runner.py`
  discovers the binary via `ERNEST_BIN` → `PATH` → repo root). The **SDK**
  (`client.py`, `async_client.py`) mirrors the HTTP API: `stream_chat`,
  `chat`, `stream_approve`, `approve`, `list_agents`, `list_teams`,
  `stream_team`, `run_team`, `list_workflows`, `stream_workflow`,
  `run_workflow`, `list_sessions`, `get_session`, `delete_session`,
  `get_run_trace`, `health`. See [PYTHON.md](PYTHON.md).

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
