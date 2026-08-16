# ernest (Python package)

Author multi-agent crews in Python — run them on a fast Go engine.

This package ships two things:

1. **A typed authoring DSL** — `Agent`, `Task`, `Crew`, `Team`, `Guard`.
   Your Python file compiles to `ernest.json` and executes on the real
   ernest binary (`python -m ernest run`): same run loop, same streaming
   events as the Go API. Orchestration is *not* reimplemented in Python.
2. **Sync + async SDK clients** — chat, streaming, human-in-the-loop
   approvals, teams, workflows, runs and traces against a running ernest
   server. Stdlib-only, no third-party dependencies.

## Install

```bash
pip install ernest
```

The DSL's `run`/`doctor` commands need the `ernest` binary (Go ≥ 1.26):
it is discovered via the `ERNEST_BIN` env var, `PATH`, or by building
the repo root. See [PYTHON.md](../../docs/PYTHON.md) for details.

## Author a crew

```python
# crew.py
from ernest import Agent, Task, Crew

researcher = Agent("researcher", provider="mock",
                   instructions="You research topics.")
writer = Agent("writer", provider="mock",
               instructions="You write clearly.")

crew = Crew(
    name="py-crew",
    tasks=[
        Task(researcher, "Research {{input}}", name="research"),
        Task(writer, "Write from {{research}}", name="write",
             depends_on=["research"]),
    ],
)
```

```bash
python -m ernest doctor crew.py --json        # validate + print compiled config
python -m ernest run crew.py --input "quantum chips" --json
```

`provider="mock"` is keyless and deterministic — demos and CI run
anywhere. Swap to `provider="compatible"` + `model`/`base_url`/
`api_key_env` for a real model (OpenRouter, Groq, Ollama, vLLM…).

## Drive a server

```python
from ernest import ErnestClient, AsyncErnestClient

client = ErnestClient("http://127.0.0.1:9090")
for event in client.stream_chat("assistant", "What is 17 * 23?"):
    if event.type == "message.delta":
        print(event.delta, end="")

# human-in-the-loop
for event in client.stream_approve("assistant", "send the email?"):
    if event.type == "approval.request":
        client.approve("assistant", event.approval.id,
                       approved=True, note="looks fine")

# teams & workflows
for ev in client.stream_team("editorial", "plan the release"):
    print(ev.type)
result = client.run_workflow("pipeline", "Go concurrency")
```

## Documentation

- [PYTHON.md](../../docs/PYTHON.md) — full DSL + SDK reference
- [GUIDE.md](../../docs/GUIDE.md) — the ernest framework
- [example crew](../../examples/python-crew) — agents, team + workflow in one file
