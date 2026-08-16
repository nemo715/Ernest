# ernest User Guide

This guide follows the workflow a real user follows after installing ernest:
build → scaffold → configure → validate → run. Everything here uses the public
CLI (`ernest init`, `ernest new`, `ernest run`, `ernest playground`,
`ernest doctor`, `ernest eval`, `ernest replay`, `ernest mcp-serve`) and the declarative
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
| `ernest new knowledge [dir]` | RAG assistant: `docs/` + ingestion + retrieval chat |
| `ernest new quantum [dir]` | Multi-agent research pipeline with `orchestrate.py` |

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
| `teams` | Optional. Config-driven teams (leader + members + process) |
| `workflows` | Optional. Config-driven workflows (steps with agents/prompts/deps) |
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
`-json`, `-no-memory`. With `teams`/`workflows` in the config you can also run
orchestration directly from the CLI:

```bash
ernest.exe run -team editorial -input "plan the release"
ernest.exe run -workflow pipeline -input "Go concurrency" --json
```

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

The web UI is a full **dev console**, not just a chat window: runs & traces
(with the exact context the model saw — assembled system prompt, retrieved
knowledge chunks, history window), sessions, the HITL approvals queue,
the failures feed and the audit log. See the sidebar in the app.

## 5. Teams & workflows (config-driven)

`ernest.json` can declare teams and workflows directly — no Go code needed.
A **team** runs its members under a leader (delegation) or in a fixed
sequence; a **workflow** is a dependency-ordered pipeline of steps, each
step an agent + prompt.

```json
{
  "agents": [
    { "name": "lead", "provider": "mock", "model": "mock-1", "instructions": "You coordinate the team." },
    { "name": "researcher", "provider": "mock", "model": "mock-1", "instructions": "You research topics." },
    { "name": "writer", "provider": "mock", "model": "mock-1", "instructions": "You write clearly." }
  ],
  "teams": [
    {
      "name": "editorial",
      "leader": "lead",
      "members": ["researcher", "writer"],
      "process": "sequential"
    }
  ],
  "workflows": [
    {
      "name": "pipeline",
      "steps": [
        { "name": "research", "agent": "researcher", "prompt": "Research {{input}}" },
        { "name": "write", "agent": "writer", "prompt": "Write from {{research}}", "dependsOn": ["research"] }
      ]
    }
  ]
}
```

Team fields: `name`, `leader`, `members` (agent names), `process`
(`hierarchical` default — the leader delegates via a built-in `delegate`
tool, or `sequential` — members run in declaration order, each output
feeding the next), plus optional `maxIterations` and `instructions`.
Workflow step fields: `name`, `agent`, `prompt` (with `{{input}}` and
`{{stepName}}` placeholders), `dependsOn`. Steps without dependencies may
run concurrently.

Run them from the CLI or the server:

```bash
ernest.exe run -team editorial -input "plan the release" --json
ernest.exe run -workflow pipeline -input "Go concurrency" --json

# server: GET /api/teams, GET /api/workflows
#         POST /api/teams/{name}/run, POST /api/workflows/{name}/run  (SSE)
```

Team runs stream `delegate.start`/`delegate.end` events per member;
workflow runs stream `step.start`/`step.end` events. Team run metadata
carries `team`, `process` and the member list; workflow metadata carries
the workflow name and step count, and the result state maps step names
to their outputs.

> **Go API**: the same orchestration is available programmatically in
> `github.com/nemo715/Ernest/team` and `github.com/nemo715/Ernest/workflow`;
> the `ernest new team` / `ernest new workflow` templates scaffold that
> wiring as a standalone Go module.

> **Python**: define teams/workflows as plain Python with the DSL — see
> [docs/PYTHON.md](PYTHON.md) (`python -m ernest run crew.py`).

## 6. Tools

Built-in tools are enabled per agent via `"tools"`:

| Name | What it does | Default policy |
|---|---|---|
| `calculator` | Safe arithmetic expression evaluator | Runs freely |
| `http_fetch` | GET a URL and return the text | Runs freely |
| `now` | Current UTC time | Runs freely |
| `web_search` | DuckDuckGo HTML search (no API key) | Runs freely |
| `file_read`, `file_list` | Read / list files inside the agent's `toolSandbox` | Runs freely (sandbox only) |
| `file_write` | Write / append a file inside the agent's `toolSandbox` | Requires approval (opt out via `toolPolicy.autoApprove`) |
| `shell_exec` | Run a shell command inside the agent's `toolSandbox` | Disabled by default (`toolPolicy.enableShell`); **always** requires approval, never auto-approvable, audit-logged |
| `browser_navigate`, `browser_read`, `browser_click`, `browser_type`, `browser_screenshot` | Drive a shared headless Edge/Chrome window (CDP, lazy launch) | Requires approval by default (opt out via `toolPolicy.autoApprove`) |
| `browser` | Legacy single-tool browser (action enum) | Runs freely |
| `a2a_call` | Call another agent (used by teams) | — |

```json
"tools": ["calculator", "http_fetch", "now", "file_read", "file_write", "web_search"]
```

### Sandbox + policy

File and shell tools only touch the agent's `toolSandbox` directory
(relative paths resolve inside it; absolute paths and `..` escapes are
rejected). Tune approvals per agent with `toolPolicy`:

```json
{
  "name": "worker",
  "tools": ["file_read", "file_write", "file_list", "web_search", "shell_exec"],
  "toolSandbox": "sandbox",
  "toolPolicy": {
    "enableShell": true,
    "autoApprove": ["file_write"],
    "requireApproval": ["web_search"]
  }
}
```

`enableShell` gates `shell_exec` (off by default); `autoApprove` exempts
tools from the default approval set (but can never include `shell_exec`);
`requireApproval` adds extra approval-gated tools. `ernest doctor` warns
when `shell_exec` is enabled. Everything else — databases, SaaS APIs,
anything — attaches via **MCP servers** (next section).

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

`ernest eval` is a regression harness: declarative scenarios check an
agent's behaviour, and a saved baseline turns quality drift into a
non-zero CI exit. Two layers per scenario:

- **Deterministic assertions** (free, run anywhere, mock in CI): status,
  output substrings, tool calls and their arguments.
- **LLM-as-judge** (scores quality on 0..1 against a rubric): a second
  model call grades the output. Uses the agent's provider — mock keeps
  it deterministic, real models judge real answers. `judge.model`
  overrides the judging model.

Scenario files are JSON — a `{"scenarios": [...]}` document, or a single
scenario; point at a file or a directory:

```json
{
  "scenarios": [
    {
      "name": "math-exact",
      "input": "What is 17 * 23? Use the calculator tool.",
      "expect": {
        "status": "completed",
        "toolCalls": [
          { "name": "calculator", "argsContains": { "expression": "17*23" } }
        ],
        "outputContains": ["391"]
      }
    },
    {
      "name": "quality-explains-math",
      "input": "Explain in 2-3 sentences why 17 * 23 = 391.",
      "expect": { "status": "completed" },
      "judge": {
        "rubric": "Must state 391, show 17*23, and explain the multiplication clearly.",
        "minScore": 0.7
      }
    }
  ]
}
```

Expectations: `status` (`completed` | `awaiting_approval` | `failed`),
`outputContains`, `noToolCalls`, `toolCalls` (with `argsContains` —
string arguments are compared whitespace-insensitively, so real-model
formatting like `"17 * 23"` still matches `"17*23"`). Judge requires
`rubric`; `minScore` defaults to 0.7. Tool-argument matches and judge
scores are reported per scenario, together with tokens and estimated
cost, so evals double as a cost ledger.

### Context assertions (what the model actually saw)

Every run records the context that was assembled for it: the system
prompt (instructions + retrieved knowledge chunks) and how much history
was sent. `contextContains` asserts on that — it catches the failure
mode where instructions or knowledge silently stop reaching the model:

```json
{
  "name": "instructions-reach-the-model",
  "input": "What are the refund rules?",
  "expect": {
    "status": "completed",
    "contextContains": ["You are a helpful assistant.", "refund policy"]
  }
}
```

Each entry must appear in the assembled system prompt; a run that
captured no context, or a prompt missing a required substring, fails
with the reason. You can see the exact assembled prompt per run in the
console (Runs & traces → a run → Context tab) and via
`GET /api/runs/{id}/trace` (`context` field).

```bash
# run scenarios (exits non-zero on any failure)
ernest eval --config ernest.json --agent assistant --scenarios eval-cases

# record the current results as a baseline (per config/provider!)
ernest eval --config ernest.json --scenarios eval-cases --update-baseline

# CI gate: diff against the baseline, exit non-zero on regressions
ernest eval --config ernest.json --scenarios eval-cases --baseline eval-baseline.json
```

Regression rules: a scenario that passed in the baseline and fails now
is a regression (exit non-zero); so is a scenario that vanished from the
suite. Judge-score moves of >= 0.25 are reported as quality deltas even
when the scenario still passes. `--json` prints the full summary (agent,
model, per-scenario tokens/cost/judge, regressions) for dashboards.

Keep separate baseline files per provider: a mock-mode baseline cannot
be compared against real-model runs (the scenarios differ). The mock
provider makes `ernest eval` fully deterministic in CI — same provider
family as the agent, so judge scenarios script cleanly.

### Tool-result shape checks (the "silent 200 OK" catcher)

A tool that returns an empty array or an empty string looks like a 200
OK to monitoring — the next node just types around the void. Shape
checks make those deterministic eval assertions, not vibes:

```json
{
  "name": "search-returns-results",
  "input": "Find recent pricing pages.",
  "expect": {
    "status": "completed",
    "toolResults": [
      { "name": "search", "shape": { "minItems": 1 } },
      { "name": "fetch", "shape": { "requiredFields": ["title"], "fieldTypes": { "title": "string" } } },
      { "name": "db_query", "errorContains": "sql" }
    ]
  }
}
```

`shape` supports `requiredFields`, `fieldTypes` (`string | number |
int | bool | array | object`), `minItems` (arrays) and `minLength`
(strings). `errorContains` asserts the tool **failed** with a matching
error — the expected-failure flip side. Both apply to the tool result
content (double-encoded JSON is unwrapped automatically). No matching
tool result for a named expectation is itself a failure.

### Self-updating suite: learn scenarios from production failures

Your golden dataset should grow from real user behaviour, not manual
curation. Point the server at a failures feed and every failed run is
appended automatically — no SDK changes, no manual step:

```json
{ "agent": "assistant", "failures": "failures.jsonl" }
```

What gets captured per failed run: the input, output, status, error and
the tool calls/results leading up to the failure. (`ernest run
--failures-out file.jsonl` appends the same records from the CLI.) Then:

```bash
# turn failures into scenarios, merged into scenarios/generated.json
ernest eval --config ernest.json --learn failures.jsonl

# …and optionally generate an LLM judge rubric per scenario (costs tokens)
ernest eval --config ernest.json --learn failures.jsonl --learn-judge
```

Learning is deterministic and idempotent: each failure is fingerprinted
(input + failing tool), deduped against the current suite and against
already-learned records, capped at `--learn-max` (default 50) per run,
and merged into `generated.json` next to your hand-written scenarios —
so re-running `--learn` never duplicates. The generated scenarios use
only deterministic assertions (status, `errorContains`, shape `minItems`
/ `minLength`), so the suite stays runnable in CI on the mock provider.
The same run then evaluates the grown suite, and the merged file counts
as a regression suite for the baseline gate.

### Nightly replay against live production

Evals don't have to wait for a deploy. `ernest replay` runs the same
suite (same assertions, same baseline engine) against a **live** ernest
server over HTTP, so golden datasets act as monitoring assets against
runtime drift:

```bash
# replay the suite against the deployed agent
ernest replay --endpoint http://prod.internal:9090 --agent assistant --scenarios eval-cases

# diff against the saved baseline and alert on drift
ernest replay --endpoint http://prod.internal:9090 --baseline eval-baseline.json

# post the drift report to a webhook (Slack/ops) when the replay finishes
ernest replay --endpoint http://prod.internal:9090 --baseline eval-baseline.json --webhook https://hooks.example.com/report

# machine-readable report for dashboards
ernest replay --endpoint http://prod.internal:9090 --json
```

Exit codes mirror `ernest eval`: non-zero on failed scenarios or
baseline regressions, so the same command works as a nightly cron job
and as a deploy-time gate. The report adds the endpoint, per-scenario
status, and a drift section vs the baseline (new failures, new
regressions, judge-score moves). Judge scoring runs through the **local**
config's provider, so the suite evaluates the deployed agent in your own
model family without calling the server's model twice. `--update-baseline`
records the live run as the new baseline. Each scenario runs
`skipMemory: true` with a per-scenario timeout (default 120s,
`--timeout`).

## 10. Python: authoring + SDK

ernest is Python-first too. Write crews, teams and workflows in plain
Python (DSL), compile them to `ernest.json`, and run them through the
same Go engine:

```python
# crew.py
from ernest import Agent, Task, Crew

researcher = Agent("researcher", provider="mock", instructions="You research topics.")
writer = Agent("writer", provider="mock", instructions="You write clearly.")
research = Task(researcher, "Research {{input}}", name="research")
write = Task(writer, "Write from {{research}}", name="write", depends_on=["research"])
crew = Crew(name="py-crew", tasks=[research, write])
```

```bash
pip install ./python
python -m ernest run crew.py --input "quantum chips" --json
python -m ernest doctor crew.py
```

Full authoring reference (DSL shapes, teams, workflows, validation,
`ERNEST_BIN` discovery): [docs/PYTHON.md](PYTHON.md).

### SDK clients

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

### Push traces from any framework

ernest isn't the only agent runtime in your stack. `POST /api/traces`
accepts traces from any framework — LangChain, CrewAI, your own
harness — and stores them in the same store as native runs, so
`GET /api/traces/{id}` (and the legacy `/api/runs/{id}/trace`) returns
them identically (spans carry `"source": "ingested"`). A stdlib-only
snippet (no SDK required):

```python
import json
import urllib.request

trace = {
    "traceId": "tr-abc123",
    "name": "langchain-agent",
    "agent": "support",
    "status": "failed",
    "startedAt": "2026-08-02T00:00:00Z",
    "durationMs": 3420,
    "spans": [
        {"id": "sp-1", "runId": "tr-abc123", "name": "llm", "kind": "llm",
         "status": "ok", "startedAt": "2026-08-02T00:00:00Z", "durationMs": 2100},
        {"id": "sp-2", "runId": "tr-abc123", "name": "tool:search", "kind": "tool",
         "status": "error", "input": "{\"q\": \"pricing\"}",
         "output": "{\"error\": \"empty result\"}",
         "startedAt": "2026-08-02T00:00:00Z", "durationMs": 400},
    ],
    "metrics": {"iterations": 3, "tokens": 842, "costCents": 0.9},
}
req = urllib.request.Request(
    "http://127.0.0.1:9090/api/traces",
    data=json.dumps(trace).encode(),
    headers={"Content-Type": "application/json"},
)
with urllib.request.urlopen(req) as resp:
    assert resp.status == 202
```

Ingestion is capped at 4 MB and 2000 spans per trace (HTTP 202 on
accept, 400 on a malformed payload).

## 11. Expose agents to other tools

```bash
ernest.exe mcp-serve --config ernest.json
# stdio transport — Claude Desktop, Cursor, any MCP client

ernest.exe mcp-serve --config ernest.json --http :8123
# streamable HTTP transport (2025-06-18) — curl, remote clients
```

Serves every configured agent as a **tool** over MCP. `--name` sets the
server name. The server advertises tools, resources and prompts:
`resources/list` is honestly empty (no static resources), and
`prompts/list` exposes a `chat` template (input required, optional
agent override) that expands to a user message — handy for MCP hosts
that surface prompts to users. Clients can read the same surface: the
Go API (and the Python SDK's HTTP client) can list tools, call them,
list resources, read resources, list prompts and fetch a prompt.

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
| `GET /api/teams`, `GET /api/workflows` | Orchestration registry |
| `POST /api/teams/{name}/run`, `POST /api/workflows/{name}/run` | Team / workflow runs (SSE) |
| `POST /api/chat` | Streaming chat (SSE) |
| `POST /api/approve` | Approve/deny a pending tool call |
| `GET /api/sessions`, `GET/DELETE /api/sessions/{id}` | Session store |
| `GET /api/runs` | Run summary list (newest first) |
| `GET /api/runs/{id}/trace` | Per-run trace (spans, metrics, context) |
| `POST /api/traces` | Ingest a trace from any framework (202) |
| `GET /api/failures?limit=N` | Failure feed (JSONL tail, max 200) |
| `GET /api/audit` | Audit log |
| `GET /ws/chat` | WebSocket chat |
| `GET /.well-known/agent.json` | A2A discovery document |
| `POST /a2a/{agent}`, `GET /a2a/{agent}/card` | A2A JSON-RPC & agent card |
