"""Live tests: real OpenRouter model through the real Go engine.

Skipped unless OPENROUTER_API_KEY is set (and the ernest binary is built):

    go build -o ernest ./cmd/ernest
    pytest python/tests/test_live.py -v

Three surfaces are exercised end-to-end:
1. the Python SDK client (SSE chat, sequential team, workflow DAG)
   against ``ernest playground`` serving a real-provider config;
2. the authoring DSL compiled and run via ``python -m ernest run``.
"""

from __future__ import annotations

import json
import os
import socket
import subprocess
import time
import urllib.request
from pathlib import Path
from typing import Any, Dict

import pytest

from ernest import Client
from ernest.dsl import Agent, Crew, Task

pytestmark = pytest.mark.skipif(
    not os.environ.get("OPENROUTER_API_KEY"),
    reason="OPENROUTER_API_KEY not set — live tests skipped",
)

REPO = Path(__file__).resolve().parents[2]
LIVE_MODEL = "openai/gpt-4o-mini"

# One config with real providers for the agent, both team members and
# the workflow steps — the same shape users write in ernest.json.
LIVE_CONFIG: Dict[str, Any] = {
    "agents": [
        {
            "name": "assistant",
            "description": "Live model assistant",
            "provider": "compatible",
            "model": LIVE_MODEL,
            "baseUrl": "https://openrouter.ai/api/v1",
            "apiKeyEnv": "OPENROUTER_API_KEY",
            "instructions": "You are a concise assistant. Answer in one sentence.",
        },
        {
            "name": "lead",
            "provider": "compatible",
            "model": LIVE_MODEL,
            "baseUrl": "https://openrouter.ai/api/v1",
            "apiKeyEnv": "OPENROUTER_API_KEY",
            "instructions": "You coordinate the team.",
        },
        {
            "name": "researcher",
            "provider": "compatible",
            "model": LIVE_MODEL,
            "baseUrl": "https://openrouter.ai/api/v1",
            "apiKeyEnv": "OPENROUTER_API_KEY",
            "instructions": "You research facts and state them plainly, numbers included.",
        },
        {
            "name": "writer",
            "provider": "compatible",
            "model": LIVE_MODEL,
            "baseUrl": "https://openrouter.ai/api/v1",
            "apiKeyEnv": "OPENROUTER_API_KEY",
            "instructions": "You condense findings into one short sentence, keeping any numbers.",
        },
    ],
    "teams": [
        {
            "name": "editorial",
            "leader": "lead",
            "members": ["researcher", "writer"],
            "process": "sequential",
        }
    ],
    "workflows": [
        {
            "name": "pipeline",
            "steps": [
                {"name": "research", "agent": "researcher", "prompt": "Find the answer to {{input}} and state it."},
                {"name": "write", "agent": "writer", "prompt": "Condense: {{research}}", "dependsOn": ["research"]},
            ],
        }
    ],
}


def _bin() -> str:
    for candidate in (REPO / "ernest.exe", REPO / "ernest"):
        if candidate.is_file():
            return str(candidate)
    pytest.skip("ernest binary not built (go build -o ernest ./cmd/ernest)")


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


@pytest.fixture(scope="module")
def live_server(tmp_path_factory) -> str:
    """Boot `ernest playground` with the live config and yield its base URL."""
    cfg = tmp_path_factory.mktemp("live-cfg") / "ernest.json"
    cfg.write_text(json.dumps(LIVE_CONFIG), encoding="utf-8")
    port = _free_port()
    proc = subprocess.Popen(
        [_bin(), "playground", "--config", str(cfg), "--port", str(port)],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        cwd=str(cfg.parent),
    )
    base = f"http://127.0.0.1:{port}"
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            out = proc.stdout.read().decode(errors="replace") if proc.stdout else ""
            pytest.fail(f"ernest playground exited early: {out}")
        try:
            urllib.request.urlopen(base + "/healthz", timeout=1).read()
            break
        except Exception:
            time.sleep(0.2)
    else:
        proc.kill()
        pytest.fail("ernest playground did not become healthy")
    yield base
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()


def test_live_chat_over_sse(live_server: str) -> None:
    """The SDK client streams a real-model chat to completion over SSE."""
    result = Client(live_server).chat("assistant", "What is the capital of France?")
    assert result.status == "completed", result.error
    assert "paris" in result.output.lower()
    assert result.usage and result.usage.output_tokens > 0


def test_live_sequential_team(live_server: str) -> None:
    """A config-declared sequential team runs both members on the real model."""
    result = Client(live_server).run_team("editorial", "what is 6*7?")
    assert result.status == "completed", result.error
    assert "42" in result.output


def test_live_workflow_pipeline(live_server: str) -> None:
    """A config-declared workflow DAG runs research -> write on the real model."""
    result = Client(live_server).run_workflow("pipeline", "what is 6*7?")
    assert result.status == "completed", result.error
    assert "42" in result.output


def test_live_dsl_crew(tmp_path) -> None:
    """A Python-authored crew with real providers runs on the Go engine."""
    researcher = Agent(
        "researcher",
        provider="compatible",
        model=LIVE_MODEL,
        base_url="https://openrouter.ai/api/v1",
        api_key_env="OPENROUTER_API_KEY",
        instructions="You research facts and state them plainly, numbers included.",
    )
    writer = Agent(
        "writer",
        provider="compatible",
        model=LIVE_MODEL,
        base_url="https://openrouter.ai/api/v1",
        api_key_env="OPENROUTER_API_KEY",
        instructions="You condense findings into one short sentence, keeping any numbers.",
    )
    research = Task(researcher, "Find the answer to {{input}} and state it.", name="research")
    write = Task(writer, "Condense: {{research}}", name="write", depends_on=["research"])
    crew = Crew(name="live-crew", tasks=[research, write])

    # Compile the DSL to ernest.json (the same path `python -m ernest run`
    # takes) and execute it on the real Go engine.
    from ernest.runner import write_project

    write_project(crew.to_config(), tmp_path)
    proc = subprocess.run(
        [_bin(), "run", "--config", str(tmp_path / "ernest.json"),
         "--workflow", "live-crew", "--input", "what is 6*7?", "--json"],
        capture_output=True,
        text=True,
        cwd=str(tmp_path),
        env={**os.environ},
        timeout=180,
    )
    assert proc.returncode == 0, proc.stdout + proc.stderr
    out = json.loads(proc.stdout)
    assert out.get("status") == "completed", out
    assert "42" in json.dumps(out.get("output", out))
