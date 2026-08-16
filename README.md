# ernest

A real-time multi-agent framework in Go. One static binary serves a streaming
HTTP/SSE API, a WebSocket chat channel, and a web playground — with tool use,
human-in-the-loop approvals, guardrails, tracing, evals, MCP and A2A support,
plus a Python SDK and a Next.js UI.

**Built for chat-style UX**: events stream as they happen (token deltas, tool
calls, delegation, approvals, metrics) over SSE or WebSocket, so clients render
live progress instead of waiting for a final answer.

## What's inside

| Layer | What you get |
|---|---|
| **Agents** | Declarative `ernest.json` config, built-in tools, tool-loop with iteration cap |
| **Teams & workflows** | Config-driven teams (hierarchical / sequential) and workflow DAGs — author in `ernest.json` **or** Python DSL, run via CLI / HTTP / Go / Python |
| **Human-in-the-loop** | Tool calls that pause for approval; approve/deny over REST or SDK |
| **Guardrails** | Token & cost caps, deny-lists, always-require-approval tools, input redaction |
| **Memory & storage** | Session store (in-memory or SQLite), agent memory, knowledge base |
| **Streaming** | SSE (`/api/chat`) + WebSocket (`/ws/chat`) with interrupt/steer |
| **Tracing & audit** | Per-run trace endpoint + `POST /api/traces` ingestion from any framework, full audit log; traces carry the exact context the model saw (system prompt + knowledge + history window) |
| **Interop** | MCP client (stdio/http) & server mode (`mcp-serve` stdio or streamable HTTP), A2A server with agent cards |
| **Eval** | Scenario runner (`ernest eval`) with judge, baseline gates, tool-result shape checks, `contextContains` context assertions; `--learn` grows the suite from production failures; `replay --endpoint` runs it nightly against a live server with drift alerts |
| **Dev console** | Next.js console served by the runtime: runs & traces (waterfall + context panel), sessions, HITL approvals queue, failures feed, audit log |
| **CLI** | `init`, `new`, `run`, `playground`, `doctor`, `eval`, `replay`, `mcp-serve` |
| **SDKs** | Python (sync + async), examples in Go; Next.js playground UI |

## Built-in tools

Attach tools per agent via `"tools": [...]` in `ernest.json`.

| Tool | What it does | Default policy |
|---|---|---|
| `calculator` | Safe arithmetic (no eval) | Runs freely |
| `http_fetch` | Fetch a URL as LLM-safe plain text | Runs freely |
| `now` | Current UTC time | Runs freely |
| `web_search` | DuckDuckGo HTML search (no API key) — plain web results only, no news/news-feed/SERP coverage | Runs freely |
| `file_read` / `file_list` | Read / list files inside the agent's `toolSandbox` | Runs freely (sandbox only) |
| `file_write` | Write or append a file inside the agent's `toolSandbox` | Always requires approval (opt out via `toolPolicy.autoApprove`) |
| `shell_exec` | Run a shell command inside the agent's `toolSandbox` | Disabled by default (`toolPolicy.enableShell`); **always** requires approval and is audit-logged — can never be auto-approved |
| `browser_navigate` / `browser_read` / `browser_click` / `browser_type` / `browser_screenshot` | Drive a shared headless Edge/Chrome window | Approval-gated by default (opt out via `toolPolicy.autoApprove`) |
| `browser` | Legacy single-tool browser (action enum) | Runs freely |

Sandboxed packs (`file_*`, `shell_exec`) refuse to touch anything outside the
agent's `toolSandbox` directory (absolute paths and `..` escapes are rejected).
Everything else — databases, SaaS APIs, anything — is available through
**MCP servers** (`mcpServers` in `ernest.json`, stdio or HTTP); that is the
escape hatch rather than adding unmaintainable built-ins.

```json
{
  "agents": [
    {
      "name": "worker",
      "provider": "mock",
      "model": "mock-1",
      "tools": ["file_read", "file_write", "file_list", "web_search", "shell_exec"],
      "toolSandbox": "sandbox",
      "toolPolicy": {
        "enableShell": true,
        "autoApprove": ["file_write"]
      }
    }
  ]
}
```

## Quickstart

Requires Go 1.26+.

```bash
git clone https://github.com/nemo715/Ernest.git
cd Ernest
go build -o ernest.exe ./cmd/ernest
```

### 1. Scaffold a project

```bash
./ernest.exe init            # writes ernest.json with a mock agent (no key needed)
./ernest.exe doctor          # validates config + connectivity
./ernest.exe run -input "6*7?"   # mock agent answers instantly
```

### 2. Use a real model

Edit `ernest.json` — swap the agent's provider to an OpenAI-compatible endpoint:

```json
{
  "agents": [
    {
      "name": "assistant",
      "description": "General assistant",
      "provider": "compatible",
      "model": "openai/gpt-4o-mini",
      "baseUrl": "https://openrouter.ai/api/v1",
      "apiKeyEnv": "OPENROUTER_API_KEY",
      "instructions": "You are a helpful assistant.",
      "tools": ["calculator", "http_fetch", "now"]
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
```

Set the key in the environment (ernest never auto-loads `.env`):

```powershell
# PowerShell
$env:OPENROUTER_API_KEY = "sk-or-v1-..."
```
```bash
# bash/zsh
export OPENROUTER_API_KEY="sk-or-v1-..."
```

Then run — or open the playground:

```bash
./ernest.exe doctor
./ernest.exe run -input "what time is it in UTC?"
./ernest.exe playground --port 9090    # http://127.0.0.1:9090
```

### 3. Build a team

```bash
./ernest.exe new team examples/desk    # scaffold: ernest.json + main.go + go.mod
cd examples/desk
go mod tidy
go run .                               # live delegation transcript on the terminal
```

The scaffolded project is its own Go module and imports ernest from the
published module path, so it compiles **anywhere** — no need to be inside
this repo. With `OPENROUTER_API_KEY` set, the same scaffold runs on a real
model (gpt-4o-mini via OpenRouter) and **the model decides delegation
itself**; without a key it falls back to scripted mock providers, so it also
runs offline and deterministically for demos and CI.

### 4. Teams & workflows (config-driven)

Declare multi-agent teams and DAG workflows in `ernest.json` — no Go code,
no Python — then run them from the CLI or over HTTP:

```json
{
  "agents": [
    { "name": "lead", "provider": "mock", "model": "mock-1", "instructions": "You lead the team." },
    { "name": "researcher", "provider": "mock", "model": "mock-1", "instructions": "You research." },
    { "name": "writer", "provider": "mock", "model": "mock-1", "instructions": "You write." }
  ],
  "teams": [
    {
      "name": "editorial",
      "process": "sequential",
      "leader": "lead",
      "members": ["researcher", "writer"]
    }
  ],
  "workflows": [
    {
      "name": "pipeline",
      "steps": [
        { "name": "research", "agent": "researcher", "prompt": "Research {{input}}" },
        { "name": "write", "agent": "writer", "prompt": "Write a report from {{research}}", "dependsOn": ["research"] }
      ]
    }
  ]
}
```

```bash
./ernest.exe run -team editorial -input "plan the release"
./ernest.exe run -workflow pipeline -input "quantum chips" --json
```

Teams stream `delegate.start/end` per member; workflows stream `step.start/end`
and run independent steps concurrently, with optional LLM-judged `guard`s and
`retries` per step. The same surfaces are exposed over HTTP
(`GET /api/teams`, `GET /api/workflows`, `POST .../run` → SSE), the public Go
packages (`team`, `workflow`), and the Python SDK/DSL. See
[GUIDE.md §5](docs/GUIDE.md) and [PYTHON.md](docs/PYTHON.md).

### 5. Example apps

| App | What it shows | Dir |
|---|---|---|
| **Desk** | 3-agent team (lead → researcher, writer) with live delegation | `examples/desk` |
| **Research Lab** | 4-agent PhD team (PI, reviewer, analyst, writer) + SSE dashboard UI | `examples/research-lab` |
| **CLAW** | Local AI worker (files, shell, browser) with HITL approval UI | `examples/claw` |
| **py-crew** | Crew authored in Python (DSL: agents + team + workflow), run on the Go engine | `examples/python-crew` |

The Go examples are each their own module and import ernest from the
published module path; `py-crew` runs through `python -m ernest`. With
`OPENROUTER_API_KEY` set they run on a real model; without it they fall
back to scripted mock providers.

## Python

ernest is Python-first: author crews in Python and run them on the Go engine,
or drive a running server with the SDK.

```bash
pip install ./python
```

**Author in Python, run on Go** — the DSL compiles to `ernest.json` and
executes on the same binary:

```python
# crew.py
from ernest import Agent, Task, Crew

researcher = Agent("researcher", provider="mock", instructions="You research topics.")
writer = Agent("writer", provider="mock", instructions="You write clearly.")

crew = Crew(
    name="py-crew",
    tasks=[
        Task(researcher, "Research {{input}}", name="research"),
        Task(writer, "Write from {{research}}", name="write", depends_on=["research"]),
    ],
)
```

```bash
python -m ernest run crew.py --input "quantum chips" --json
python -m ernest doctor crew.py --json      # validate + print compiled config
```

**Drive a running server** with the SDK:

```python
from ernest import ErnestClient

client = ErnestClient("http://127.0.0.1:9090")
for event in client.stream_chat("assistant", "What is 17 * 23?"):
    if event.type == "message.delta":
        print(event.delta, end="")
```

Full DSL reference, `python -m ernest` usage and the SDK method list:
[docs/PYTHON.md](docs/PYTHON.md).

## Performance — honest numbers

Full methodology and raw data: [TEST.md](TEST.md). The short version:

| Measurement | ernest | langchain-core 1.5.3 | Comparable? |
|---|---|---|---|
| Framework overhead, mock LLM, 1 turn | 17.6 µs | 494 µs | Partly — different languages & work |
| Binary / dependency size | 20.4 MB | 42 MB | Yes (cold start / CI) |
| Import-to-ready / process boot | 17–19 ms | 235 ms | Yes |
| Real model (gpt-4o-mini), 1 turn | ~0.9–1.1 s | ~0.9–1.1 s | Yes — network-bound parity |

**Honest interpretation**: the framework itself is fast, but with a real model
the model latency dominates — ernest's framework tax is roughly **0.2% per
turn**. The speed matters for evals, batch runs, cold starts, and concurrency —
**not** for per-chat UX. LangChain genuinely wins on ecosystem, community, and
language familiarity; ernest's advantages are a single static binary, event
streaming as a first-class citizen, and zero-python/dependency setup.

Not measured: multi-agent orchestration at scale, memory/vector performance,
RAG pipelines.

## Documentation

- [User guide](docs/GUIDE.md) — install → config → run → teams/workflows → tools → eval → production
- [Python](docs/PYTHON.md) — authoring DSL (`Agent`/`Task`/`Crew`/`Team`), `python -m ernest`, SDK clients
- [Architecture](docs/ARCHITECTURE.md) — packages, wire format, event lifecycle
- [Comparison](docs/COMPARISON.md) — ernest vs CrewAI, honest and dated
- [Plan](PLAN.md) and [Test report](TEST.md) — development history & verification

## Status & honest limitations

- Public Go API: `agent`, `core`, `llm`, `team`, `workflow`, `server`,
  `storage` (module `github.com/nemo715/Ernest`, tagged `v0.1.7`).
  `ernest init` / `ernest new <agent|team|workflow|server>` scaffold
  standalone compilable projects.
- `examples/`, `bench/` and `eval-demo/` import `ernest/internal/*` and
  compile inside this repository only — the internal packages are not
  importable from outside (Go's `internal/` rule).
- Guardrails (token/cost caps, deny-lists, redaction) are set **in Go code**
  today; they are not yet `ernest.json` fields.
- Config store types: `memory` (default) and `sqlite`. A PostgreSQL backend
  exists in `internal/storage` but is not yet wired into config validation.

## License

See [LICENSE](LICENSE) (add one before publishing releases).
