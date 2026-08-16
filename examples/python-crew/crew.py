"""py-crew — a multi-agent crew authored entirely in Python.

The DSL (Agent / Team / Task / Crew) compiles to the ernest.json schema
and runs on the Go engine — same run loop, same streaming events as a
hand-written config file. Nothing here is a Python reimplementation of
the orchestration.

Run it (keyless + deterministic with the mock provider):

    python -m ernest doctor crew.py --json
    python -m ernest run crew.py --input "quantum chips" --json
    python -m ernest run crew.py --team editorial --input "quantum chips"

To use a real model, swap each agent's provider to "compatible" and add
model / base_url / api_key_env, e.g.:

    Agent("researcher", provider="compatible",
          model="openai/gpt-4o-mini",
          base_url="https://openrouter.ai/api/v1",
          api_key_env="OPENROUTER_API_KEY", ...)
"""

from ernest import Agent, Crew, Task, Team

# --- Agents ---------------------------------------------------------------
# provider="mock" needs no key and answers deterministically — perfect for
# demos and CI. The ernest binary is discovered via ERNEST_BIN, PATH, or
# the repository root (see docs/PYTHON.md).

researcher = Agent(
    "researcher",
    provider="mock",
    instructions="You research topics and return concise findings with sources.",
    tools=["web_search", "file_read", "file_write"],
    tool_sandbox="sandbox",
    max_iterations=8,
)

writer = Agent(
    "writer",
    provider="mock",
    instructions="You turn research findings into clear, structured prose.",
    tools=["file_write"],
    tool_sandbox="sandbox",
)

editor = Agent(
    "editor",
    provider="mock",
    instructions="You lead the editorial team and approve final copy.",
)

# --- Team -----------------------------------------------------------------
# Sequential process: members run in declaration order, each output feeding
# the next — no leader model call, fully deterministic.

editorial = Team(
    name="editorial",
    leader=editor,
    members=[researcher, writer],
    process="sequential",
)

# --- Workflow -------------------------------------------------------------
# Tasks compile into a workflow DAG named after the crew. When no task
# declares depends_on, tasks chain in declaration order.

crew = Crew(
    name="py-crew",
    teams=[editorial],
    tasks=[
        Task(researcher, "Research {{input}}", name="research"),
        Task(
            writer,
            "Write a short report from {{research}}",
            name="write",
            depends_on=["research"],
        ),
    ],
)
