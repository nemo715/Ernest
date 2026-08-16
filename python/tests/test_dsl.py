"""Unit tests for the authoring DSL (``ernest.dsl``).

Every ``to_config()`` output is validated against the real Go validator
(``ernest doctor``) so the Python DSL and the ernest.json schema cannot
drift apart (the round-trip test at the bottom).
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from typing import Any, Dict

import pytest

from ernest.dsl import Agent, Crew, Guard, Task, Team

REPO = Path(__file__).resolve().parents[2]


# ---------------------------------------------------------------------------
# Agent
# ---------------------------------------------------------------------------


def test_agent_defaults() -> None:
    cfg = Agent("researcher").to_config()
    assert cfg == {"name": "researcher", "provider": "mock", "model": "mock-1"}


def test_agent_full_config() -> None:
    a = Agent(
        "writer",
        provider="openai",
        model="gpt-4o-mini",
        instructions="Turn notes into prose.",
        description="Content writer",
        tools=["calculator", "now"],
        memory=False,
        knowledge={"path": "docs", "maxChunks": 4},
        max_iterations=9,
    )
    cfg = a.to_config()
    assert cfg["name"] == "writer"
    assert cfg["provider"] == "openai"
    assert cfg["model"] == "gpt-4o-mini"
    assert cfg["instructions"] == "Turn notes into prose."
    assert cfg["description"] == "Content writer"
    assert cfg["tools"] == ["calculator", "now"]
    assert cfg["memory"] is False
    assert cfg["knowledge"] == {"path": "docs", "maxChunks": 4}
    assert cfg["maxIterations"] == 9


def test_agent_tool_sandbox_and_policy() -> None:
    """File/shell tools require toolSandbox; policy passes through verbatim."""
    a = Agent(
        "worker",
        tools=["file_read", "shell_exec"],
        tool_sandbox="sandbox",
        tool_policy={"enableShell": True, "autoApprove": ["file_write"]},
    )
    cfg = a.to_config()
    assert cfg["toolSandbox"] == "sandbox"
    assert cfg["toolPolicy"] == {"enableShell": True, "autoApprove": ["file_write"]}
    # Optional fields stay absent when unset.
    assert "toolSandbox" not in Agent("plain").to_config()
    assert "toolPolicy" not in Agent("plain").to_config()


def test_agent_empty_name_raises() -> None:
    with pytest.raises(ValueError, match="Agent name is required"):
        Agent("").to_config()


def test_agent_model_default_for_mock_only() -> None:
    assert Agent("a", provider="mock").to_config()["model"] == "mock-1"


def test_agent_real_provider_requires_model() -> None:
    """Mirrors the Go validator: non-mock providers need a model id."""
    with pytest.raises(ValueError, match="model is required for provider 'openai'"):
        Agent("a", provider="openai").to_config()
    assert Agent("a", provider="openai", model="gpt-4o-mini").to_config()["model"] == "gpt-4o-mini"


# ---------------------------------------------------------------------------
# Guard
# ---------------------------------------------------------------------------


def test_guard_to_config() -> None:
    assert Guard("answer must cite sources").to_config() == {
        "rubric": "answer must cite sources",
        "minScore": 0.7,
    }


def test_guard_custom_min_score() -> None:
    assert Guard("be brief", min_score=0.9).to_config()["minScore"] == 0.9


def test_guard_empty_rubric_raises() -> None:
    with pytest.raises(ValueError, match="Guard rubric is required"):
        Guard("").to_config()


# ---------------------------------------------------------------------------
# Task
# ---------------------------------------------------------------------------


def test_task_defaults_step_name_from_agent() -> None:
    researcher = Agent("researcher")
    cfg = Task(researcher, "Find facts").to_config()
    assert cfg == {"name": "researcher", "agent": "researcher", "prompt": "Find facts"}


def test_task_with_refs_guard_and_retries() -> None:
    researcher = Agent("researcher")
    writer = Agent("writer")
    first = Task(researcher, "Dig", name="research")
    cfg = Task(
        writer,
        "Write up: {{research}}",
        name="write",
        guard=Guard("cover the findings", min_score=0.8),
        depends_on=[first, "outline"],
        retries=3,
    ).to_config()
    assert cfg == {
        "name": "write",
        "agent": "writer",
        "prompt": "Write up: {{research}}",
        "guard": {"rubric": "cover the findings", "minScore": 0.8},
        "dependsOn": ["research", "outline"],
        "retries": 3,
    }


def test_task_depends_on_task_objects() -> None:
    first = Task(Agent("a"), "one", name="first")
    second = Task(Agent("b"), "two", depends_on=[first])
    assert second.to_config()["dependsOn"] == ["first"]


# ---------------------------------------------------------------------------
# Team
# ---------------------------------------------------------------------------


def test_team_default_hierarchical() -> None:
    leader = Agent("lead")
    cfg = Team(leader, [Agent("researcher"), Agent("writer")], name="editorial").to_config()
    assert cfg == {
        "name": "editorial",
        "leader": "lead",
        "members": ["researcher", "writer"],
        "process": "hierarchical",
    }


def test_team_sequential_with_metadata() -> None:
    cfg = Team(
        Agent("lead"),
        [Agent("a")],
        name="line",
        process="sequential",
        description="a line, not a tree",
        instructions="go in order",
        max_iterations=5,
    ).to_config()
    assert cfg["process"] == "sequential"
    assert cfg["description"] == "a line, not a tree"
    assert cfg["instructions"] == "go in order"
    assert cfg["maxIterations"] == 5


def test_team_invalid_process_raises() -> None:
    team = Team(Agent("lead"), [Agent("a")], name="t", process="anarchy")
    with pytest.raises(ValueError, match="unknown process"):
        team.to_config()


def test_team_without_members_raises() -> None:
    with pytest.raises(ValueError, match="at least one member"):
        Team(Agent("lead"), [], name="t").to_config()


def test_team_agent_refs_collects_agents() -> None:
    leader, a, b = Agent("lead"), Agent("a"), Agent("b")
    team = Team(leader, [a, "b"], name="t")
    refs = team.agent_refs()
    assert {r.name for r in refs} == {"lead", "a"}  # string refs are config-level


# ---------------------------------------------------------------------------
# Crew
# ---------------------------------------------------------------------------


def _simple_crew() -> Crew:
    return Crew(
        "pipeline",
        agents=[
            Agent("researcher", instructions="Find facts."),
            Agent("writer", instructions="Write prose."),
        ],
        tasks=[
            Task("researcher", "Research: {{input}}", name="research"),
            Task("writer", "Summarize: {{research}}", name="write"),
        ],
    )


def test_crew_to_config_shape() -> None:
    cfg = _simple_crew().to_config()
    assert sorted(cfg.keys()) == ["agents", "store", "workflows"]
    assert cfg["store"] == {"type": "memory"}
    assert {a["name"] for a in cfg["agents"]} == {"researcher", "writer"}
    wf = cfg["workflows"][0]
    assert wf["name"] == "pipeline"
    assert wf["steps"][0] == {"name": "research", "agent": "researcher", "prompt": "Research: {{input}}"}
    # Auto-chaining: each step depends on the previous one.
    assert wf["steps"][1]["dependsOn"] == ["research"]


def test_crew_explicit_deps_disable_auto_chain() -> None:
    crew = Crew(
        "graph",
        agents=[Agent("a"), Agent("b"), Agent("c")],
        tasks=[
            Task("a", "one", name="a"),
            Task("b", "two", name="b", depends_on=["a"]),
            Task("c", "three", name="c", depends_on=["a"]),
        ],
    )
    steps = crew.to_config()["workflows"][0]["steps"]
    assert [s["name"] for s in steps] == ["a", "b", "c"]
    assert steps[1]["dependsOn"] == ["a"]
    assert steps[2]["dependsOn"] == ["a"]


def test_crew_with_team_and_workflow() -> None:
    lead, researcher, writer = Agent("lead"), Agent("researcher"), Agent("writer")
    crew = Crew(
        "newsroom",
        agents=[lead, researcher, writer],
        teams=[Team(lead, [researcher, writer], name="editorial", process="sequential")],
        tasks=[
            Task(researcher, "dig: {{input}}", name="dig"),
            Task(writer, "write: {{dig}}", name="draft"),
        ],
    )
    cfg = crew.to_config()
    assert sorted(cfg.keys()) == ["agents", "store", "teams", "workflows"]
    assert cfg["teams"][0]["name"] == "editorial"
    assert cfg["teams"][0]["process"] == "sequential"
    assert cfg["workflows"][0]["name"] == "newsroom"


def test_crew_collects_team_and_task_agents() -> None:
    """Agents referenced only by teams/tasks join the config implicitly."""
    crew = Crew(
        "implicit",
        teams=[Team(Agent("lead"), [Agent("worker")], name="t")],
        tasks=[Task(Agent("solo"), "hi", name="step")],
    )
    cfg = crew.to_config()
    assert {a["name"] for a in cfg["agents"]} == {"lead", "worker", "solo"}


def test_crew_duplicate_agent_raises() -> None:
    crew = Crew("dup", agents=[Agent("a"), Agent("a")])
    with pytest.raises(ValueError, match="duplicate agent"):
        crew.to_config()


def test_crew_without_agents_raises() -> None:
    with pytest.raises(ValueError, match="crew has no agents"):
        Crew("empty").to_config()


def test_crew_team_missing_name_raises() -> None:
    crew = Crew("c", teams=[Team(Agent("lead"), [Agent("a")])])
    with pytest.raises(ValueError, match="Team name is required"):
        crew.to_config()


def test_crew_unknown_team_leader_raises() -> None:
    crew = Crew("c", agents=[Agent("a")], teams=[Team("ghost", ["a"], name="t")])
    with pytest.raises(ValueError, match="unknown leader"):
        crew.to_config()


def test_crew_unknown_team_member_raises() -> None:
    crew = Crew("c", agents=[Agent("lead")], teams=[Team("lead", ["ghost"], name="t")])
    with pytest.raises(ValueError, match="unknown member"):
        crew.to_config()


def test_crew_duplicate_team_raises() -> None:
    team = Team(Agent("lead"), [Agent("a")], name="t")
    crew = Crew("c", teams=[team, Team(Agent("lead2"), [Agent("b")], name="t")])
    with pytest.raises(ValueError, match="duplicate team"):
        crew.to_config()


def test_crew_unknown_task_agent_raises() -> None:
    """A string ref that names no declared agent is rejected."""
    crew = Crew("c", agents=[Agent("a")], tasks=[Task("ghost", "hi")])
    with pytest.raises(ValueError, match="unknown agent"):
        crew.to_config()


def test_crew_unknown_dependency_raises() -> None:
    crew = Crew(
        "c",
        agents=[Agent("a")],
        tasks=[Task("a", "hi", name="a", depends_on=["missing"])],
    )
    with pytest.raises(ValueError, match="depends on unknown step"):
        crew.to_config()


def test_crew_duplicate_task_names_raise() -> None:
    crew = Crew(
        "c",
        agents=[Agent("a"), Agent("b")],
        tasks=[Task("a", "one"), Task("b", "two", name="a")],
    )
    with pytest.raises(ValueError, match="duplicate task/step"):
        crew.to_config()


def test_crew_dependency_cycle_raises() -> None:
    crew = Crew(
        "loop",
        agents=[Agent("a"), Agent("b")],
        tasks=[
            Task("a", "one", name="a", depends_on=["b"]),
            Task("b", "two", name="b", depends_on=["a"]),
        ],
    )
    with pytest.raises(ValueError, match="cycle"):
        crew.to_config()


def test_crew_self_cycle_raises() -> None:
    crew = Crew(
        "loop",
        agents=[Agent("a")],
        tasks=[Task("a", "one", name="a", depends_on=["a"])],
    )
    with pytest.raises(ValueError, match="cycle"):
        crew.to_config()


def test_crew_empty_name_raises() -> None:
    with pytest.raises(ValueError, match="Crew name is required"):
        Crew("").to_config()


# ---------------------------------------------------------------------------
# Round-trip against the Go validator (ernest doctor)
# ---------------------------------------------------------------------------


def _doctor(config: Dict[str, Any], binary: str) -> subprocess.CompletedProcess:
    """Run ``ernest doctor`` on a config dict (absolute paths, cwd in repo)."""
    tmp = Path.cwd() / ".pytest-dsl-roundtrip.json"
    tmp.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    try:
        return subprocess.run(
            [binary, "doctor", "--config", str(tmp.resolve())],
            cwd=str(REPO),
            capture_output=True,
            text=True,
            timeout=120,
        )
    finally:
        tmp.unlink(missing_ok=True)


def test_round_trip_crew_passes_go_doctor(ernest_bin: str) -> None:
    proc = _doctor(_simple_crew().to_config(), ernest_bin)
    assert proc.returncode == 0, proc.stdout + proc.stderr


def test_round_trip_team_passes_go_doctor(ernest_bin: str) -> None:
    lead, a, b = Agent("lead"), Agent("researcher"), Agent("writer")
    crew = Crew("newsroom", agents=[lead, a, b], teams=[Team(lead, [a, b], name="editorial")])
    proc = _doctor(crew.to_config(), ernest_bin)
    assert proc.returncode == 0, proc.stdout + proc.stderr


def test_round_trip_workflow_with_guard_passes_go_doctor(ernest_bin: str) -> None:
    """A guarded workflow is schema-valid even though the mock provider
    cannot judge it at runtime (the Go engine is what runs it)."""
    crew = Crew(
        "guarded",
        agents=[Agent("writer")],
        tasks=[Task("writer", "summarize {{input}}", guard=Guard("be concise"))],
    )
    proc = _doctor(crew.to_config(), ernest_bin)
    assert proc.returncode == 0, proc.stdout + proc.stderr
