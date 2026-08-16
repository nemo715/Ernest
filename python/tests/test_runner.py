"""Tests for ``ernest.runner`` and the ``python -m ernest`` CLI.

End-to-end tests execute the real Go binary (``ernest_bin`` fixture) with
absolute ``--config`` paths from inside the repo — the same two steps a
user performs by hand: write ernest.json, run the binary.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional
from unittest import mock

import pytest

import ernest.runner as runner
from ernest.dsl import Agent, Crew, Task, Team
from ernest.types import RunResult

REPO = Path(__file__).resolve().parents[2]
PKG = Path(__file__).resolve().parents[1]  # .../python (the ernest package lives here)


# ---------------------------------------------------------------------------
# Binary discovery
# ---------------------------------------------------------------------------


def test_find_ernest_bin_env_var(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    fake = tmp_path / "fake-ernest.exe"
    fake.touch()
    monkeypatch.setenv("ERNEST_BIN", str(fake))
    assert runner.find_ernest_bin() == str(fake)


def test_find_ernest_bin_path_before_repo(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    fake = tmp_path / "ernest-on-path.exe"
    fake.touch()
    monkeypatch.delenv("ERNEST_BIN", raising=False)
    monkeypatch.setattr(runner.shutil, "which", lambda _name: str(fake))
    assert runner.find_ernest_bin() == str(fake)


def test_find_ernest_bin_repo_root(monkeypatch: pytest.MonkeyPatch, ernest_bin: str) -> None:
    """Without ERNEST_BIN/PATH, discovery falls back to the repo root."""
    monkeypatch.delenv("ERNEST_BIN", raising=False)
    monkeypatch.setattr(runner.shutil, "which", lambda _name: None)
    assert Path(runner.find_ernest_bin()).resolve() == Path(ernest_bin).resolve()


def test_find_ernest_bin_missing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("ERNEST_BIN", raising=False)
    monkeypatch.setattr(runner.shutil, "which", lambda _name: None)
    monkeypatch.setattr(Path, "is_file", lambda _self: False)
    with pytest.raises(FileNotFoundError, match="ERNEST_BIN"):
        runner.find_ernest_bin()


# ---------------------------------------------------------------------------
# Project writing
# ---------------------------------------------------------------------------


def test_write_project_writes_config_and_env(tmp_path: Path) -> None:
    config: Dict[str, Any] = {
        "agents": [
            {"name": "a", "provider": "openai", "model": "gpt-4o-mini"},
            {"name": "b", "provider": "compatible", "model": "x", "apiKeyEnv": "MY_KEY"},
            {"name": "c", "provider": "mock", "model": "mock-1"},
        ]
    }
    cfg_path = runner.write_project(config, tmp_path)
    assert cfg_path == tmp_path / "ernest.json"
    assert json.loads(cfg_path.read_text(encoding="utf-8")) == config

    env = (tmp_path / ".env.example").read_text(encoding="utf-8")
    assert "OPENAI_API_KEY=" in env
    assert "MY_KEY=" in env
    # apiKeyEnv overrides the provider default; each key listed once.
    assert env.count("MY_KEY=") == 1


def test_write_project_mock_only_env_comment(tmp_path: Path) -> None:
    config: Dict[str, Any] = {"agents": [{"name": "a", "provider": "mock", "model": "mock-1"}]}
    runner.write_project(config, tmp_path)
    env = (tmp_path / ".env.example").read_text(encoding="utf-8")
    assert env == "# no API keys needed (mock providers)\n"


def test_write_project_no_overwrite(tmp_path: Path) -> None:
    runner.write_project({"agents": []}, tmp_path)
    with pytest.raises(FileExistsError):
        runner.write_project({"agents": []}, tmp_path, overwrite=False)


# ---------------------------------------------------------------------------
# run_ernest against the real binary
# ---------------------------------------------------------------------------


def test_run_ernest_version(ernest_bin: str) -> None:
    proc = runner.run_ernest(["version"], binary=ernest_bin, cwd=REPO, capture=True)
    assert proc.returncode == 0
    assert "ernest" in proc.stdout


def test_run_ernest_invalid_command_fails(ernest_bin: str) -> None:
    proc = runner.run_ernest(["no-such-command"], binary=ernest_bin, cwd=REPO, capture=True)
    assert proc.returncode != 0
    assert "unknown command" in proc.stderr


def _mock_crew_config() -> Dict[str, Any]:
    """Two mock agents chained into a workflow — keyless, deterministic."""
    crew = Crew(
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
    return crew.to_config()


def _run_cli(binary: str, config: Dict[str, Any], args: List[str]) -> subprocess.CompletedProcess:
    """Run the ernest binary with an absolute config path, cwd inside the repo."""
    cfg_path = REPO / ".pytest-runner-roundtrip.json"
    cfg_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    try:
        return runner.run_ernest(
            [*args, "--config", str(cfg_path.resolve()), "--json"],
            binary=binary,
            cwd=REPO,
            capture=True,
        )
    finally:
        cfg_path.unlink(missing_ok=True)


def test_run_workflow_end_to_end(ernest_bin: str) -> None:
    """DSL config → Go workflow engine → parseable RunResult."""
    proc = _run_cli(ernest_bin, _mock_crew_config(), ["run", "--workflow", "pipeline", "--input", "Go concurrency"])
    assert proc.returncode == 0, proc.stderr
    result = RunResult.from_dict(json.loads(proc.stdout))
    assert result is not None
    assert result.completed, proc.stdout
    assert result.status == "completed"
    # The workflow output is the shared state JSON: research + write keys.
    state = json.loads(result.output)
    assert set(state.keys()) == {"input", "research", "write"}
    assert state["input"] == "Go concurrency"


def test_run_sequential_team_end_to_end(ernest_bin: str) -> None:
    """A sequential team runs member outputs without a leader call."""
    lead, a, b = Agent("lead"), Agent("researcher"), Agent("writer")
    crew = Crew(
        "newsroom",
        agents=[lead, a, b],
        teams=[Team(lead, [a, b], name="line", process="sequential")],
    )
    proc = _run_cli(ernest_bin, crew.to_config(), ["run", "--team", "line", "--input", "topic"])
    assert proc.returncode == 0, proc.stderr
    result = RunResult.from_dict(json.loads(proc.stdout))
    assert result is not None
    assert result.completed
    assert result.metadata.get("process") == "sequential"


def test_run_unknown_team_fails(ernest_bin: str) -> None:
    proc = _run_cli(ernest_bin, _mock_crew_config(), ["run", "--team", "ghost", "--input", "x"])
    assert proc.returncode != 0
    assert 'team "ghost" not found' in proc.stderr


def test_doctor_rejects_invalid_config(ernest_bin: str) -> None:
    cfg_path = REPO / ".pytest-runner-invalid.json"
    cfg_path.write_text(json.dumps({"agents": [{"name": ""}]}), encoding="utf-8")
    try:
        proc = runner.run_ernest(
            ["doctor", "--config", str(cfg_path.resolve())], binary=ernest_bin, cwd=REPO, capture=True
        )
    finally:
        cfg_path.unlink(missing_ok=True)
    assert proc.returncode != 0


# ---------------------------------------------------------------------------
# run_team / run_workflow / run_agent argument construction
# ---------------------------------------------------------------------------


def test_run_helpers_build_cli_args(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: List[tuple] = []
    monkeypatch.setattr(
        runner,
        "_run_and_parse",
        lambda config, args, binary=None, cwd=None: calls.append((config, args, binary, cwd)),
    )
    cfg: Dict[str, Any] = {"agents": []}
    runner.run_team(cfg, "t", "in", binary="B", cwd=Path("c"))
    runner.run_workflow(cfg, "w", "in", binary="B", cwd=Path("c"))
    runner.run_agent(cfg, "a", "in", binary="B", cwd=Path("c"))
    assert calls == [
        (cfg, ["run", "--team", "t", "--input", "in"], "B", Path("c")),
        (cfg, ["run", "--workflow", "w", "--input", "in"], "B", Path("c")),
        (cfg, ["run", "--agent", "a", "--input", "in"], "B", Path("c")),
    ]


# ---------------------------------------------------------------------------
# python -m ernest CLI
# ---------------------------------------------------------------------------


def _write_crew_file(tmp_path: Path, body: str = "") -> Path:
    crew_file = tmp_path / "crew.py"
    crew_file.write_text(
        body
        or (
            "from ernest.dsl import Agent, Crew, Task\n"
            "researcher = Agent('researcher', instructions='Find facts.')\n"
            "writer = Agent('writer', instructions='Write prose.')\n"
            "crew = Crew('pipeline', tasks=[\n"
            "    Task(researcher, 'Research: {{input}}', name='research'),\n"
            "    Task(writer, 'Summarize: {{research}}', name='write'),\n"
            "])\n"
        ),
        encoding="utf-8",
    )
    return crew_file


def _cli_env(binary: str) -> Dict[str, str]:
    env = os.environ.copy()
    env["ERNEST_BIN"] = binary
    env["PYTHONPATH"] = str(PKG) + os.pathsep + env.get("PYTHONPATH", "")
    return env


def test_cli_run_crew_end_to_end(ernest_bin: str, tmp_path: Path) -> None:
    crew_file = _write_crew_file(tmp_path)
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(crew_file), "--input", "Go concurrency", "--json"],
        cwd=str(REPO),
        env=_cli_env(ernest_bin),
        capture_output=True,
        text=True,
        timeout=180,
    )
    assert proc.returncode == 0, proc.stderr
    result = RunResult.from_dict(json.loads(proc.stdout))
    assert result is not None
    assert result.completed
    assert "research" in result.output


def test_cli_run_team_end_to_end(ernest_bin: str, tmp_path: Path) -> None:
    crew_file = tmp_path / "crew.py"
    crew_file.write_text(
        "from ernest.dsl import Agent, Team\n"
        "lead = Agent('lead')\n"
        "team = Team(lead, [Agent('a')], name='line', process='sequential')\n",
        encoding="utf-8",
    )
    # A module-level `team` object is detected and run directly.
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(crew_file), "--input", "topic", "--json"],
        cwd=str(REPO),
        env=_cli_env(ernest_bin),
        capture_output=True,
        text=True,
        timeout=180,
    )
    assert proc.returncode == 0, proc.stderr
    result = RunResult.from_dict(json.loads(proc.stdout))
    assert result is not None
    assert result.completed
    assert result.metadata.get("process") == "sequential"


def test_cli_doctor_prints_config(ernest_bin: str, tmp_path: Path) -> None:
    crew_file = _write_crew_file(tmp_path)
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "doctor", str(crew_file), "--json"],
        cwd=str(REPO),
        env=_cli_env(ernest_bin),
        capture_output=True,
        text=True,
        timeout=180,
    )
    assert proc.returncode == 0, proc.stderr
    compiled = json.loads(proc.stdout)
    assert compiled["workflows"][0]["name"] == "pipeline"


def test_cli_missing_crew_file(tmp_path: Path) -> None:
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(tmp_path / "nope.py"), "--input", "x"],
        cwd=str(REPO),
        env=_cli_env("unused"),
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert proc.returncode == 1
    assert "python -m ernest:" in proc.stderr


def test_cli_no_crew_object(tmp_path: Path) -> None:
    crew_file = _write_crew_file(tmp_path, body="x = 1\n")
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(crew_file), "--input", "x"],
        cwd=str(REPO),
        env=_cli_env("unused"),
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert proc.returncode == 1
    assert "no module-level `crew`, `team` or `workflow`" in proc.stderr


def test_cli_missing_input(tmp_path: Path) -> None:
    crew_file = _write_crew_file(tmp_path)
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(crew_file)],
        cwd=str(REPO),
        env=_cli_env("unused"),
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert proc.returncode == 1
    assert "--input is required" in proc.stderr


def test_cli_ambiguous_target(tmp_path: Path) -> None:
    """Multiple teams (and no tasks) with no --team/--workflow flag must
    fail loudly instead of guessing."""
    crew_file = _write_crew_file(
        tmp_path,
        body=(
            "from ernest.dsl import Agent, Crew, Team\n"
            "lead = Agent('lead')\n"
            "crew = Crew('newsroom', agents=[lead, Agent('a'), Agent('b')],\n"
            "            teams=[Team(lead, ['a'], name='t1'), Team(lead, ['b'], name='t2')])\n"
        ),
    )
    proc = subprocess.run(
        [sys.executable, "-m", "ernest", "run", str(crew_file), "--input", "x"],
        cwd=str(REPO),
        env=_cli_env("unused"),
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert proc.returncode == 1
    assert "ambiguous target" in proc.stderr


def test_cli_team_flag_picks_target(ernest_bin: str, tmp_path: Path) -> None:
    """--team selects a named team even when a workflow also exists."""
    crew_file = _write_crew_file(tmp_path)
    proc = subprocess.run(
        [
            sys.executable, "-m", "ernest", "run", str(crew_file),
            "--team", "ghost-team", "--input", "x",
        ],
        cwd=str(REPO),
        env=_cli_env(ernest_bin),
        capture_output=True,
        text=True,
        timeout=180,
    )
    # The Go engine reports the missing team (target resolution worked).
    assert proc.returncode == 1
    assert 'team "ghost-team" not found' in proc.stderr


def test_run_and_parse_raises_on_non_json_stdout(ernest_bin: str, monkeypatch: pytest.MonkeyPatch) -> None:
    """A run whose stdout is not JSON surfaces a clear RuntimeError."""
    monkeypatch.setattr(runner, "write_project", lambda config, directory: directory / "ernest.json")
    monkeypatch.setattr(
        runner,
        "run_ernest",
        lambda args, binary=None, cwd=None, timeout=300.0, capture=False: mock.Mock(
            returncode=0, stdout="not json"
        ),
    )
    with pytest.raises(RuntimeError, match="did not print a JSON result"):
        runner._run_and_parse({"agents": []}, ["run", "--agent", "a"], binary=ernest_bin, cwd=REPO)
