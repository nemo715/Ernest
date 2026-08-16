# Python with ernest

ernest is Python-first. Two ways to use it:

1. **Author in Python, run on Go** — the `ernest` package ships a small
   typed DSL (`Agent`, `Task`, `Crew`, `Team`, `Guard`). Your Python file
   compiles to `ernest.json` and executes on the real Go engine — same
   binary, same run loop, same streaming events as the Go API.
2. **Drive a running server** — sync and async SDK clients that mirror
   the HTTP API (chat, approvals, teams, workflows, runs, traces).

```bash
pip install ./python
```

---

## 1. Authoring: Python DSL → Go engine

### Agents

```python
from ernest import Agent

researcher = Agent(
    "researcher",                      # name (required, unique)
    provider="compatible",             # mock | openai | compatible | anthropic | gemini | groq | ollama
    model="openai/gpt-4o-mini",
    base_url="https://openrouter.ai/api/v1",
    api_key_env="OPENROUTER_API_KEY",
    instructions="You research topics.",
    tools=["web_search", "file_read", "file_write"],
    max_iterations=8,
    memory=True,
)
```

`provider="mock"` needs no key and answers deterministically — perfect
for demos and CI.

### Tasks → workflow

A `Task` is one workflow step (an agent + prompt). `{{input}}` and
`{{stepName}}` placeholders are replaced at run time with the run input
and earlier step outputs.

```python
from ernest import Task, Guard

research = Task(researcher, "Research {{input}}", name="research")
write = Task(
    writer,
    "Write a report from {{research}}",
    name="write",
    depends_on=[research],            # task object or step name
    guard=Guard(rubric="Must cite the research findings.", min_score=0.7),
    retries=1,
)
```

When no task declares `depends_on`, tasks chain in declaration order.

### Crew

A `Crew` collects agents + optional teams + optional task workflow and
compiles to `ernest.json`:

```python
from ernest import Agent, Task, Crew, Team

researcher = Agent("researcher", provider="mock", instructions="You research topics.")
writer = Agent("writer", provider="mock", instructions="You write clearly.")

crew = Crew(
    name="py-crew",
    teams=[
        Team(
            name="editorial",
            leader="researcher",       # Agent object or name
            members=[writer],
            process="sequential",      # or "hierarchical" (default)
        )
    ],
    tasks=[
        Task(researcher, "Research {{input}}", name="research"),
        Task(writer, "Write from {{research}}", name="write", depends_on=["research"]),
    ],
)
```

### Run it

The module-level `crew` (or `team` / `workflow`) object is detected,
compiled, validated by the Go engine, and executed:

```bash
# run the compiled workflow
python -m ernest run crew.py --input "quantum chips" --json

# pick an explicit target when the crew has several
python -m ernest run crew.py --team editorial --input "plan it"
python -m ernest run crew.py --workflow py-crew --input "plan it"

# validate + print the compiled config (validated by the Go engine)
python -m ernest doctor crew.py --json
```

The CLI discovers the `ernest` binary in this order: `ERNEST_BIN` env
var → `PATH` → repository root (`go build ./cmd/ernest`). Set
`PYTHONPATH` to the repo's `python/` dir (or `pip install ./python`)
when running from source.

### Validation (fail fast in Python)

`to_config()` raises `ValueError` with a specific message on: unknown
team process, empty team members, duplicate agent/team/step names,
references to unknown agents, dependency cycles (Kahn's check), missing
crew/team names. The Go engine re-validates everything in
`python -m ernest doctor`.

---

## 2. SDK clients

```python
from ernest import ErnestClient, AsyncErnestClient

client = ErnestClient("http://127.0.0.1:9090")

# streaming chat (SSE events: message.delta, tool.call, approval.request, ...)
for event in client.stream_chat("assistant", "What is 17 * 23?"):
    if event.type == "message.delta":
        print(event.delta, end="")

# one-shot
result = client.chat("assistant", "hello")

# human-in-the-loop
for event in client.stream_approve("assistant", "send the email?"):
    if event.type == "approval.request":
        client.approve("assistant", event.approval.id, approved=True, note="looks fine")

# registry + orchestration
agents = client.list_agents()
teams = client.list_teams()
workflows = client.list_workflows()
for ev in client.stream_team("editorial", "plan the release"):
    print(ev.type)
result = client.run_workflow("pipeline", "Go concurrency")

# observability
runs = client.list_runs()
trace = client.get_run_trace(run_id)
client.health()
```

The async client mirrors the same methods with `await`; both are
stdlib-only (no third-party dependencies).

### Typed events & errors

Events are typed `RunEvent` objects (`EVENT_TYPES` maps the wire
values); results are typed `RunResult`/`RunTrace`. Errors are typed too:
`APIError`, `BadRequestError`, `NotFoundError`, `RateLimitError`,
`ServerError`, `RunError`/`AgentError`/`ProviderError`/`ToolError`,
`ValidationError`, `RunInterrupted`, `RunTimeout`, `SSEProtocolError`.

---

## 3. Reference

| Symbol | What it is |
|---|---|
| `Agent(name, provider, model, base_url, api_key_env, instructions, description, tools, tool_sandbox, tool_policy, memory, knowledge, max_iterations)` | One agent; compiles to the `agents` array |
| `Task(agent, prompt, name, guard, depends_on, retries)` | One workflow step |
| `Guard(rubric, min_score=0.7)` | LLM-judged quality gate on a step output |
| `Team(leader, members, name, description, process, instructions, max_iterations)` | One config team (`hierarchical` \| `sequential`) |
| `Crew(name, agents, teams, tasks)` | Top-level document → `ernest.json` |
| `python -m ernest run file.py [--team N] [--workflow N] --input ... [--json]` | Compile + run on the Go engine |
| `python -m ernest doctor file.py [--json]` | Validate (Go engine) + print compiled config |
| `ErnestClient(base_url)` / `AsyncErnestClient(base_url)` | HTTP API clients |
