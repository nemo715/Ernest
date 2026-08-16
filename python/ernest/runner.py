"""Runs a Python-authored crew on the ernest Go engine (subprocess).

Honest scope: this module does **not** reimplement the agent engine. It
writes the crew's config to ``ernest.json`` (+ ``.env.example``), finds
the ``ernest`` binary and executes it — exactly the two steps a user
performs by hand. One engine, one source of truth.

Binary discovery order:

1. the ``ERNEST_BIN`` environment variable
2. ``ernest`` on ``PATH``
3. ``ernest.exe`` / ``ernest`` next to this package's repository root
   (developer convenience; see :func:`find_ernest_bin`)
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence

from .types import RunResult


def find_ernest_bin() -> str:
    """Locate the ``ernest`` binary or raise ``FileNotFoundError``.

    Honoring the ERNEST_BIN env var first, then PATH, then the repo
    root (``ernest.exe``/``ernest``) when the SDK runs from a checkout.
    """
    env = os.environ.get("ERNEST_BIN")
    if env:
        return env
    on_path = shutil.which("ernest")
    if on_path:
        return on_path
    # Developer checkout: <repo>/python/ernest/runner.py -> <repo>/.
    repo = Path(__file__).resolve().parents[2]
    for candidate in (repo / "ernest.exe", repo / "ernest"):
        if candidate.is_file():
            return str(candidate)
    raise FileNotFoundError(
        "ernest binary not found: set ERNEST_BIN or put `ernest` on PATH "
        "(build it from the repo with: go build -o ernest ./cmd/ernest)"
    )


def write_project(config: Dict[str, Any], directory: Path, overwrite: bool = True) -> Path:
    """Write ``ernest.json`` + ``.env.example`` for the given config dict.

    Returns the config path. ``.env.example`` lists the API key env vars
    the agents' providers may need (``apiKeyEnv`` or the provider's
    default), one empty assignment per line.
    """
    directory.mkdir(parents=True, exist_ok=True)
    config_path = directory / "ernest.json"
    if config_path.exists() and not overwrite:
        raise FileExistsError(f"{config_path} already exists")
    config_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")

    env_keys: List[str] = []
    defaults = {"openai": "OPENAI_API_KEY", "compatible": "OPENROUTER_API_KEY", "ollama": ""}
    for agent in config.get("agents", []):
        key = agent.get("apiKeyEnv") or defaults.get((agent.get("provider") or "").lower(), "")
        if key and key not in env_keys:
            env_keys.append(key)
    env_path = directory / ".env.example"
    env_path.write_text(
        "".join(f"{k}=\n" for k in env_keys) or "# no API keys needed (mock providers)\n",
        encoding="utf-8",
    )
    return config_path


def run_ernest(
    args: Sequence[str],
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
    timeout: float = 300.0,
    capture: bool = False,
) -> subprocess.CompletedProcess:
    """Execute ``ernest`` with ``args`` and return the completed process.

    By default stdout/stderr inherit the Python process (streaming stays
    live); ``capture=True`` collects stdout for JSON result parsing.
    """
    bin_path = binary or find_ernest_bin()
    cmd = [bin_path, *args]
    return subprocess.run(
        cmd,
        cwd=str(cwd) if cwd else None,
        timeout=timeout,
        check=False,
        capture_output=capture,
        text=bool(capture),
    )


def _run_and_parse(
    config: Dict[str, Any],
    args: Sequence[str],
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
) -> RunResult:
    """Write the config, run ``ernest run ... --json`` and parse the result."""
    directory = cwd or Path.cwd()
    write_project(config, directory)
    proc = run_ernest([*args, "--json"], binary=binary, cwd=directory, capture=True)
    if proc.returncode != 0:
        raise subprocess.CalledProcessError(proc.returncode, "ernest " + " ".join(args))
    try:
        payload = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"ernest did not print a JSON result (stdout: {proc.stdout[:200]!r})"
        ) from exc
    result = RunResult.from_dict(payload)
    if result is None:
        raise RuntimeError(f"unexpected ernest result payload: {payload!r}")
    return result


def run_team(
    config: Dict[str, Any],
    team: str,
    input: str,
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
) -> RunResult:
    """Run the named team from a config dict (``ernest run --team``)."""
    return _run_and_parse(config, ["run", "--team", team, "--input", input], binary=binary, cwd=cwd)


def run_workflow(
    config: Dict[str, Any],
    workflow: str,
    input: str,
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
) -> RunResult:
    """Run the named workflow from a config dict (``ernest run --workflow``)."""
    return _run_and_parse(config, ["run", "--workflow", workflow, "--input", input], binary=binary, cwd=cwd)


def run_agent(
    config: Dict[str, Any],
    agent: str,
    input: str,
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
) -> RunResult:
    """Run a single agent from a config dict (``ernest run --agent``)."""
    return _run_and_parse(config, ["run", "--agent", agent, "--input", input], binary=binary, cwd=cwd)


def doctor(
    config: Dict[str, Any],
    binary: Optional[str] = None,
    cwd: Optional[Path] = None,
) -> subprocess.CompletedProcess:
    """Validate a config dict with the Go validator (``ernest doctor``)."""
    directory = cwd or Path.cwd()
    config_path = write_project(config, directory)
    proc = run_ernest(["doctor", "--config", str(config_path)], binary=binary, cwd=directory)
    if proc.returncode != 0:
        raise subprocess.CalledProcessError(proc.returncode, "ernest doctor")
    return proc


def main() -> int:
    """Delegate to the ``python -m ernest`` CLI (see ``__main__.py``)."""
    from .__main__ import main as cli_main

    return cli_main(sys.argv[1:])
