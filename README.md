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
| **Teams** | Leader/member delegation with live `delegate.start/end` streaming (Go API) |
| **Human-in-the-loop** | Tool calls that pause for approval; approve/deny over REST or SDK |
| **Guardrails** | Token & cost caps, deny-lists, always-require-approval tools, input redaction |
| **Memory & storage** | Session store (in-memory or SQLite), agent memory, knowledge base |
| **Streaming** | SSE (`/api/chat`) + WebSocket (`/ws/chat`) with interrupt/steer |
| **Tracing & audit** | Per-run trace endpoint, full audit log |
| **Interop** | MCP client (stdio/http) & server mode, A2A server with agent cards |
| **Eval** | Scenario runner (`ernest eval`) for regression-testing agents |
| **CLI** | `init`, `new`, `run`, `playground`, `doctor`, `eval`, `mcp-serve` |
| **SDKs** | Python (sync + async), examples in Go; Next.js playground UI |

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
this repo. The bundled scripted mock providers show the leader delegating
and streaming the result, offline and without an API key.

## Python SDK

```bash
pip install ./python
```

```python
from ernest import ErnestClient

client = ErnestClient("http://127.0.0.1:9090")
for event in client.stream_chat("assistant", "What is 17 * 23?"):
    if event.type == "message.delta":
        print(event.delta, end="")
```

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

- [User guide](docs/GUIDE.md) — install → config → run → teams → eval → production
- [Architecture](docs/ARCHITECTURE.md) — packages, wire format, event lifecycle
- [Plan](PLAN.md) and [Test report](TEST.md) — development history & verification

## Status & honest limitations

- Public Go API: `agent`, `core`, `llm`, `team`, `workflow`, `server`,
  `storage` (module `github.com/nemo715/Ernest`, tagged `v0.1.1`).
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
