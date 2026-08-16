# py-crew

A multi-agent crew authored entirely in Python — the ernest DSL
(`Agent` / `Team` / `Task` / `Crew`) compiled to `ernest.json` and run
on the Go engine. Keyless and deterministic (mock provider), so it runs
anywhere, including CI.

## Run

```bash
# install the SDK + DSL from the repo
pip install ./python

# validate + print the compiled config (validated by the Go engine)
python -m ernest doctor crew.py --json

# run the workflow (tasks compile into a workflow DAG named "py-crew")
python -m ernest run crew.py --input "quantum chips" --json

# run the team (sequential: researcher → writer, no leader model call)
python -m ernest run crew.py --team editorial --input "quantum chips"
```

The `ernest` binary is discovered via `ERNEST_BIN`, `PATH`, or the
repository root (`go build ./cmd/ernest`).

## Real models

Swap `provider="mock"` for `provider="compatible"` on each agent:

```python
Agent(
    "researcher",
    provider="compatible",
    model="openai/gpt-4o-mini",
    base_url="https://openrouter.ai/api/v1",
    api_key_env="OPENROUTER_API_KEY",
    instructions="You research topics.",
    tools=["web_search"],
)
```

and export `OPENROUTER_API_KEY` in the environment.

## What's shown

- **Agents** with tools, instructions and iteration caps — sandboxed
  file tools (`tool_sandbox`) included.
- **Team** (`editorial`, sequential process) — members run in declaration
  order, each output feeding the next.
- **Workflow** (`py-crew`) — tasks with `{{input}}` / `{{research}}`
  placeholders and explicit `depends_on`.
- The same crew is drivable from the Go CLI, the HTTP API
  (`GET /api/teams`, `GET /api/workflows`) and the Python SDK.

See [docs/PYTHON.md](../../docs/PYTHON.md) for the full DSL reference.
