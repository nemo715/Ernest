"""``python -m ernest`` — run Python-authored crews with the Go engine.

Usage::

    python -m ernest run crew.py --input "Research X"
    python -m ernest run crew.py --team editorial --input "..." [--json]
    python -m ernest run crew.py --workflow pipeline --input "..." [--json]
    python -m ernest doctor crew.py

``crew.py`` defines the crew in plain Python (see :mod:`ernest.dsl`);
this CLI detects the module-level ``crew``, ``team`` or ``workflow``
object, compiles it to ernest.json and executes the real ``ernest``
binary (discovery: ERNEST_BIN, PATH, repo root — see
:mod:`ernest.runner`).
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

from .dsl import Crew, Team
from .runner import find_ernest_bin, run_ernest


def _load_module(path: Path):
    """Import a crew file by path (no package requirements)."""
    path = path.resolve()
    spec = importlib.util.spec_from_file_location("ernest_crew", path)
    if spec is None or spec.loader is None:
        raise ValueError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.path.insert(0, str(path.parent))
    try:
        spec.loader.exec_module(module)
    finally:
        sys.path.pop(0)
    return module


def _detect(module) -> Tuple[str, Any]:
    """Find the module-level crew/team/workflow object."""
    for attr in ("crew", "team", "workflow"):
        obj = getattr(module, attr, None)
        if obj is not None:
            return attr, obj
    raise ValueError(
        f"{module.__name__}: no module-level `crew`, `team` or `workflow` "
        "object found"
    )


def _to_config(kind: str, obj: Any) -> Dict[str, Any]:
    if kind == "crew":
        if not isinstance(obj, Crew):
            raise ValueError("`crew` must be an ernest.dsl.Crew")
        return obj.to_config()
    if kind == "team":
        if not isinstance(obj, Team):
            raise ValueError("`team` must be an ernest.dsl.Team")
        team = obj
        name = team.name or "team"
        return Crew(name=name, teams=[team]).to_config()
    # kind == "workflow": a pre-built workflow config dict (or Crew with
    # only tasks). Accept both a dict passthrough and a Crew.
    if isinstance(obj, Crew):
        return obj.to_config()
    if isinstance(obj, dict):
        return obj
    raise ValueError("`workflow` must be an ernest.dsl.Crew or a config dict")


def _pick_target(kind: str, obj: Any, config: Dict[str, Any], team: str, workflow: str) -> Tuple[str, str]:
    """Decide the run target: ("team", name) or ("workflow", name)."""
    if team:
        return "team", team
    if workflow:
        return "workflow", workflow
    teams = config.get("teams") or []
    workflows = config.get("workflows") or []
    if kind == "team":
        return "team", teams[0]["name"]
    if kind == "workflow" and workflows:
        return "workflow", workflows[0]["name"]
    if workflows and len(workflows) == 1:
        return "workflow", workflows[0]["name"]
    if teams and len(teams) == 1:
        return "team", teams[0]["name"]
    raise ValueError(
        "ambiguous target: declare exactly one team or workflow, or pass "
        f"--team/--workflow (teams: {[t['name'] for t in teams]}, "
        f"workflows: {[w['name'] for w in workflows]})"
    )


def _run(args: argparse.Namespace) -> int:
    module = _load_module(Path(args.crew_file))
    kind, obj = _detect(module)
    config = _to_config(kind, obj)
    target_kind, target_name = _pick_target(kind, obj, config, args.team, args.workflow)
    if not args.input:
        raise ValueError("--input is required")

    binary = find_ernest_bin()
    with tempfile.TemporaryDirectory(prefix="ernest-crew-") as tmp:
        cfg_path = Path(tmp) / "ernest.json"
        cfg_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
        cmd = [
            binary, "run",
            f"--{target_kind}", target_name,
            "--input", args.input,
            "--config", str(cfg_path),
        ]
        if args.json:
            cmd.append("--json")
        proc = subprocess.run(cmd, check=False)
        return proc.returncode


def _doctor(args: argparse.Namespace) -> int:
    module = _load_module(Path(args.crew_file))
    kind, obj = _detect(module)
    config = _to_config(kind, obj)
    binary = find_ernest_bin()
    with tempfile.TemporaryDirectory(prefix="ernest-crew-") as tmp:
        cfg_path = Path(tmp) / "ernest.json"
        cfg_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
        if args.json:
            # Keep stdout pure JSON: swallow the doctor report, then emit
            # the compiled config (validated by the Go engine above).
            proc = subprocess.run(
                [binary, "doctor", "--config", str(cfg_path)],
                stdout=subprocess.DEVNULL,
                check=False,
            )
            if proc.returncode == 0:
                print(json.dumps(config, indent=2))
            return proc.returncode
        proc = run_ernest(["doctor", "--config", str(cfg_path)], binary=binary)
        return proc.returncode


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m ernest",
        description="Run Python-authored ernest crews with the Go engine.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="run a crew/team/workflow from a Python file")
    run.add_argument("crew_file", help="path to the crew.py file")
    run.add_argument("--team", default="", help="team name to run (default: auto-detect)")
    run.add_argument("--workflow", default="", help="workflow name to run (default: auto-detect)")
    run.add_argument("--input", default="", help="one-shot input (required)")
    run.add_argument("--json", action="store_true", help="print the raw run result JSON")
    run.set_defaults(func=_run)

    doctor = sub.add_parser("doctor", help="validate a crew file with `ernest doctor`")
    doctor.add_argument("crew_file", help="path to the crew.py file")
    doctor.add_argument("--json", action="store_true", help="print the compiled config JSON")
    doctor.set_defaults(func=_doctor)
    return parser


def main(argv: Optional[list] = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except (ValueError, FileNotFoundError) as exc:
        print(f"python -m ernest: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
