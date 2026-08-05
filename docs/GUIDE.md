# ernest User Guide

This guide follows the workflow a real user follows after installing ernest:
build → scaffold → configure → validate → run. Everything here uses the public
CLI (`ernest init`, `ernest new`, `ernest run`, `ernest playground`,
`ernest doctor`, `ernest eval`, `ernest mcp-serve`) and the declarative
`ernest.json` format.

---

## 1. Install

Requirements: **Go 1.26+**.

```bash
git clone https://github.com/nemo715/Ernest.git
cd Ernest
go build -o ernest.exe ./cmd/ernest
```

Verify:

```bash
./ernest.exe version
```

## 2. Scaffold your first project

```bash
mkdir myapp && cd myapp
../Ernest/ernest.exe init
```

`ernest init` writes an `ernest.json` with one **mock** agent — no API key
needed. Try it immediately:

```bash
ernest.exe doctor
ernest.exe run -input "6*7?"
```

The mock provider answers instantly and deterministically, which makes it
perfect for tests, demos, and CI.

### Project templates

| Command | Creates |
|---|---|
| `ernest new agent [dir]` | Single agent: `ernest.json` + `main.go` + `go.mod` |
| `ernest new team [dir]` | Leader + members with delegation (`team.New` in Go) |
| `ernest new workflow [dir]` | Sequential workflow skeleton |
| `ernest new server [dir]` | HTTP server wiring (SSE + WS + static UI) |

> **Note**: every scaffold is its own Go module (`go.mod`) that requires
> `github.com/nemo715/Ernest` from the public module path, so it compiles
> **anywhere** — no need to be inside the ernest repo. First run needs one
> `go mod tidy` to fetch the module; the bundled mock providers need no API
> key.

## 3. Configure a real model

`ernest.json` root fields:

| Field | Meaning |
|---|---|
| `agents` | Required. One or more agent definitions |
| `mcpServers` | Optional. MCP servers exposed to agents as tools |
| `store` | Optional. `{"type": "memory"|"sqlite", "dsn": "path.db"}` |

Agent fields:

| Field | Meaning |
|---|---|
| `name` | Required, unique. Referenced by the CLI and API |
| `description` | Shown in `/api/agents`, playground, A2A cards |
| `provider` | `mock`, `openai`, `compatible`, `anthropic`, `gemini`, `groq`, `ollama` |
| `model` | Model id (not needed for `mock`) |
| `baseUrl` | For `compatible` / custom endpoints (e.g. `https://openrouter.ai/api/v1`) |
| `apiKeyEnv` | Env var holding the key. Defaults to the provider's usual var (e.g. `OPENAI_API_KEY`) when unset |
| `instructions` | System prompt |
| `temperature` | Sampling temperature |
| `maxTokens` | Max output tokens per model call |
| `maxIterations` | Tool-loop cap (default 8). Guards against runaway tool loops |
| `tools` | Built-in tool names to enable |
| `mcpServers` | Names from `mcpServers` to attach as tools |
| `memory` | `true` to persist sessions (requires `store`) |

### OpenRouter example

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
      "tools": ["calculator", "http_fetch", "now"],
      "maxIterations": 8
    }
  ],
  "store": { "type": "sqlite", "dsn": "ernest.db" }
}
```

Keys are read from the process environment — ernest does **not** auto-load
`.env` files:

```powershell
# PowerShell
$env:OPENROUTER_API_KEY = "sk-or-v1-..."
```
```bash
# bash/zsh
export OPENROUTER_API_KEY="sk-or-v1-..."
```

Validate everything before running:

```bash
ernest.exe doctor
```

## 4. Run

```bash
# Single shot, default (first) agent
ernest.exe run -input "what time is it in UTC?"

# Pick an agent
ernest.exe run -agent researcher -input "summarize https://example.com"

# JSON output (one RunResult object)
ernest.exe run -agent assistant -input "hi" -json

# Resume a session (multi-turn memory)
ernest.exe run -session <session-id> -input "and now double that"

# Skip memory for stateless calls
ernest.exe run -no-memory -input "hi"
```

Run flags: `-config` (default `ernest.json`), `-agent`, `-input`, `-session`,
`-json`, `-no-memory`.

### The playground (web UI)

```bash
ernest.exe playground --config ernest.json --port 9090
# http://127.0.0.1:9090
```

- Streams token deltas live over **WebSocket** with SSE fallback
- Shows tool calls, approvals, delegation, metrics, and the full trace
- Interrupt/steer mid-run is supported
- `--demo` boots a self-contained mock demo with no config at all
- `--static <dir>` serves a prebuilt UI bundle

## 5. Teams (delegation)

Teams are assembled in Go — `ernest.json` has no team concept. The scaffold
gives you the wiring:

```bash
ernest.exe new team examples/desk
cd examples/desk
go mod tidy   # first run only: fetches github.com/nemo715/Ernest
go run .
```

```go
// examples/desk/main.go (abridged)
cfg, _ := config.Load("ernest.json")
rt, _ := cfg.Build(nil)
defer rt.Close()

desk := team.New("desk",
    byName(rt.Agents, "lead"),
    byName(rt.Agents, "researcher"),
    byName(rt.Agents, "writer"),
)

for ev := range desk.Stream(ctx, team.TeamOptions{
    LeaderID: "lead",
    Input:    "Research X and write a short summary.",
}) {
    // ev.Type: delegate.start / delegate.end carry the member agent's
    // events in ev.Data; tool results arrive as ev.ToolResult...
}
```

The leader calls members through the `delegate` tool; delegation streams live
as `delegate.start`/`delegate.end` events. The scaffold's scripted mock
providers demonstrate this offline — the leader's scripted turn calls
`delegate`, the member answers, and the stream prints both events. Swap
`llm.NewMock` for a real provider and the model decides delegation itself.

## 6. Tools

Built-in tools are enabled per agent via `"tools"`:

| Name | What it does |
|---|---|
| `calculator` | Safe arithmetic expression evaluator |
| `http_fetch` | GET a URL and return the text |
| `now` | Current UTC time |
| `browser` | Headless Chrome (CDP, via go-rod) — **lazy**: the browser process only launches on first use |
| `a2a_call` | Call another agent (used by teams) |

```json
"tools": ["calculator", "http_fetch", "now", "browser"]
```

### MCP servers

Attach any MCP server's tools to an agent:

```json
{
  "agents": [
    { "name": "assistant", "...": "...", "mcpServers": ["filesystem"] }
  ],
  "mcpServers": [
    {
      "name": "filesystem",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    },
    {
      "name": "weather",
      "transport": "http",
      "url": "http://localhost:8000/mcp"
    }
  ]
}
```

## 7. Human-in-the-loop approvals

Two kinds of tool call blocking:

1. **Always require approval** — the agent's tool list marks tools that pause
   every run (`RequireApprovalTools` in Go).
2. **Per-run** — approve or deny from the playground UI, the REST API, or the
   Python SDK.

```python
for event in client.stream_approve("assistant", "send the email?"):
    print(event.type, event.approval.id, event.approval.tool_call.name)

client.approve("assistant", approval_id, approved=True, note="looks fine")
```

Denied tools never execute — the agent sees the denial and must adapt.

## 8. Guardrails (Go API)

Guardrails are programmatic fields on `agent.Agent` today (not yet
`ernest.json` fields):

```go
a := agent.New("assistant", provider)
a.MaxTotalTokens = 20_000      // hard cap on tokens per run
a.MaxCostCents = 0.5           // hard cap on estimated spend
a.DenyTools = []string{"browser"}            // never allowed
a.RequireApprovalTools = []string{"http_fetch"} // always HITL
a.RedactPatterns = []string{`\b\d{3}-\d{2}-\d{4}\b`} // redacted in input
a.RedactReplacement = "[SSN]"
```

## 9. Evals

Scenario files are plain JSON — one scenario per line, or a document with a
`scenarios` array; point at a file or a directory:

```json
{"name":"math","input":"6*7?","expect":{"status":"completed","noToolCalls":true}}
```

```bash
ernest.exe eval --config ernest.json --agent assistant --scenarios eval-cases
```

Expectations can assert on status (`completed`/`error`), tool call counts,
substrings, and exact outputs. Run them in CI as a regression gate.

## 10. Python SDK

```bash
pip install ./python
```

Sync and async clients mirror the HTTP API:

```python
from ernest import ErnestClient, AsyncErnestClient

# sync
client = ErnestClient("http://127.0.0.1:9090")
for ev in client.stream_chat("assistant", "hello"):
    print(ev.type, ev.delta)

# async
async def main():
    ac = AsyncErnestClient("http://127.0.0.1:9090")
    agents = await ac.list_agents()
    sessions = await ac.list_sessions("assistant")
    trace = await ac.get_run_trace(run_id)
```

## 11. Expose agents to other tools

```bash
ernest.exe mcp-serve --config ernest.json
```

Serves every configured agent as a **tool** over MCP (stdio) — Claude Desktop,
Cursor, or any MCP client can call your agents. `--name` sets the server name.

## 12. Production checklist

- [ ] `ernest doctor` passes on the target machine
- [ ] API key only in the process environment, never in `ernest.json`
- [ ] `store: sqlite` enabled if you want sessions to survive restarts
- [ ] Token/cost caps and deny-lists set in Go for untrusted tool surfaces
- [ ] Approval flow wired for anything destructive (`http_fetch` to internal
      services, file writes, email)
- [ ] Audit log reviewed (`GET /api/audit`), traces exported
      (`GET /api/runs/{id}/trace`)
- [ ] `ernest eval` scenarios in CI for every agent prompt change

## HTTP API reference

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness |
| `GET /api/agents` | Agent list |
| `POST /api/chat` | Streaming chat (SSE) |
| `POST /api/approve` | Approve/deny a pending tool call |
| `GET /api/sessions`, `GET/DELETE /api/sessions/{id}` | Session store |
| `GET /api/runs/{id}/trace` | Per-run trace |
| `GET /api/audit` | Audit log |
| `GET /ws/chat` | WebSocket chat |
| `GET /.well-known/agent.json` | A2A discovery document |
| `POST /a2a/{agent}`, `GET /a2a/{agent}/card` | A2A JSON-RPC & agent card |
