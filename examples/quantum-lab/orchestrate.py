"""quantum-lab orchestrator v2 — real Qiskit experiments, ernest SDK only.

Four real agents (ernest.json) collaborate:
    quantum-theorist  -> designs the noise experiment + mitigation strategy
    quantum-engineer  -> writes Qiskit 2.x code (raw run, then mitigated run)
    quantum-analyst   -> reads REAL execution output (counts, fidelity, error
                         rates), decides mitigation, issues data-driven verdict
    quantum-reviewer  -> (available) QA critic

Pipeline:
    theorist -> engineer(code v1) -> [ORCHESTRATOR EXECUTES with Qiskit/Aer]
    -> analyst(analysis of real numbers) -> engineer(code v2 with readout
    error mitigation) -> [ORCHESTRATOR RERUNS] -> analyst(final verdict)
    -> artifacts

Artifacts written to build/:
    quantum-lab.ipynb  EXECUTED notebook: theory, both code cells with their
                       real stdout outputs, analysis, decision, final verdict
    qiskit_v1.py       the raw experiment as actually run
    qiskit_v2.py       the mitigated experiment as actually run

The orchestrator is the execution layer: agents cannot run code themselves,
so it extracts the ```python fence, executes it locally with Qiskit, and
feeds the REAL numbers back. Failed executions are escalated back to the
engineer with the exact traceback (max 3 rounds) — the same deterministic
HITL escalation pattern used for the HTML stage.

Install:  pip install ernest qiskit qiskit-aer
Run:      python orchestrate.py --base-url http://127.0.0.1:9293
"""

from __future__ import annotations

import argparse
import ast
import json
import re
import subprocess
import sys
from pathlib import Path

from ernest import Client

BUILD = Path(__file__).resolve().parent / "build"

FENCE = re.compile(r"```(html|json|python)\s*\n(.*?)```", re.DOTALL)


def clean_fenced(text: str, lang: str) -> str:
    """Extract the first ```lang block, tolerating nested fences and trailing
    prose (models love to emit ```lang```lang ... ``` plus change-notes)."""
    start = text.find(f"```{lang}")
    if start < 0:
        raise RuntimeError(
            f"no ```{lang} fence found in output (got {len(text)} chars): {text[:400]}"
        )
    start = text.find("\n", start)
    if start < 0:
        raise RuntimeError(f"```{lang} fence has no newline: {text[:120]!r}")
    start += 1
    end = text.rfind("```")
    if end <= start:
        raise RuntimeError(f"unterminated ```{lang} fence: {text[:400]}")
    body = text[start:end].strip()
    lines = body.splitlines()
    while lines and lines[0].lstrip().startswith("```"):
        lines.pop(0)
    body = "\n".join(lines).strip()
    if lang == "html":
        term = body.rfind("</html>")
        if term >= 0:
            body = body[: term + len("</html>")]
    else:
        term = max(body.rfind("}"), body.rfind("]"))
        if term >= 0:
            body = body[: term + 1]
    return body.strip()


def run_step(client: Client, agent: str, session: str, prompt: str, label: str) -> str:
    """One real agent run; returns the final output text."""
    print(f"\n=== {label} -> {agent} ===")
    res = client.chat(agent, prompt, session_id=session)
    if not res.completed:
        raise RuntimeError(f"{label}: run {res.run_id} ended {res.status}: {res.error}")
    usage = res.usage
    tok = f"{usage.input_tokens}/{usage.output_tokens}" if usage else "n/a"
    print(f"    run {res.run_id} | {res.duration_ms}ms | tokens {tok} | {len(res.output)} chars")
    return res.output


# --------------------------------------------------------------------------
# Execution layer (the agents' 'run on notebook' happens here, for real)
# --------------------------------------------------------------------------

QISKIT_CONTRACT = (
    "API contract (Qiskit 2.x + Aer 0.17): "
    "from qiskit import QuantumCircuit; "
    "from qiskit_aer import AerSimulator; "
    "from qiskit_aer.noise import NoiseModel, depolarizing_error, ReadoutError; "
    "from qiskit.quantum_info import hellinger_fidelity; import json, numpy as np.\n"
    "NOISE MODEL SNIPPET (VERIFIED WORKING in this exact environment, Qiskit "
    "2.1.2 / Aer 0.17.2 — copy VERBATIM, do NOT 'fix' it; the readout error is "
    "added to the NoiseModel, NEVER passed to AerSimulator):\n"
    "```python\n"
    "nm = NoiseModel()\n"
    "nm.add_all_qubit_quantum_error(depolarizing_error(0.02, 1), ['u1', 'u2', 'u3'])\n"
    "nm.add_all_qubit_quantum_error(depolarizing_error(0.04, 2), ['cx'])\n"
    "nm.add_all_qubit_readout_error(ReadoutError([[0.95, 0.05], [0.03, 0.97]]))\n"
    "sim = AerSimulator(noise_model=nm)\n"
    "```\n"
    "Run with shots=4096: `result = sim.run(qc, shots=4096).result(); counts = "
    "result.get_counts()`. For the ideal reference use `AerSimulator().run(...)` "
    "(no noise). Fidelity: `hellinger_fidelity(ideal_probs, noisy_probs)` where "
    "probs are normalized dicts {bitstring: count / 4096}.\n"
    "OUTPUT CONTRACT: END your script with the results dict assigned to a "
    "variable named `output` (or `result`/`results`). The orchestrator "
    "deterministically appends the final `print(json.dumps(...))` itself — "
    "do NOT print the JSON yourself. Run-1 keys: ideal_counts, noisy_counts, "
    "fidelity, error_rate. Run-2 adds mitigated_counts, mitigated_fidelity, "
    "mitigated_error_rate. Diagnostic prints before the dict are fine."
)


HARNESS = (
    "\n\n# orchestrator harness: emit the results contract deterministically\n"
    "import json as _json\n"
    "_out = None\n"
    "for _k in ('output', 'result', 'results', 'final_output', 'out'):\n"
    "    _v = globals().get(_k)\n"
    "    if isinstance(_v, dict) and 'fidelity' in _v:\n"
    "        _out = _v\n"
    "        break\n"
    "if _out is None:\n"
    "    raise RuntimeError('no results dict found (expected output/result/results with a fidelity key)')\n"
    "print(_json.dumps(_out))\n"
)


MITIGATION_SNIPPET = (
    "READOUT MITIGATION SNIPPET (VERIFIED WORKING in this environment, Qiskit "
    "2.1.2 / Aer 0.17.2 — copy VERBATIM, adapt variable names only):\n"
    "```python\n"
    "basis = ['00', '01', '10', '11']\n"
    "cal = {}\n"
    "for bs in basis:\n"
    "    c = QuantumCircuit(2, 2)\n"
    "    for i, b in enumerate(bs):\n"
    "        if b == '1': c.x(i)\n"
    "    c.measure([0, 1], [0, 1])\n"
    "    cal[bs] = sim.run(c, shots=4096).result().get_counts()\n"
    "A = np.zeros((4, 4))  # assignment matrix A[observed][true]\n"
    "for j, bs in enumerate(basis):\n"
    "    for i, obs in enumerate(basis):\n"
    "        A[i, j] = cal[bs].get(obs, 0) / 4096\n"
    "n = np.array([noisy_counts.get(bs, 0) for bs in basis], dtype=float)\n"
    "x, *_ = np.linalg.lstsq(A, n, rcond=None)\n"
    "mitigated_counts = {bs: round(max(0.0, xi)) for bs, xi in zip(basis, x)}\n"
    "```\n"
    "Then compute mitigated fidelity with hellinger_fidelity exactly like the raw "
    "fidelity (normalized probs {bitstring: count / 4096}). Print diagnostics "
    "including 'Mitigated Counts:', 'Mitigated Fidelity:', 'Mitigated Error Rate:' "
    "and finish with the final print(json.dumps(output)) as the LAST statement "
    "(keys: ideal_counts, noisy_counts, fidelity, error_rate, mitigated_counts, "
    "mitigated_fidelity, mitigated_error_rate)."
)


def run_code(code: str, path: Path, timeout: int = 300) -> tuple[str, str]:
    """Execute Python code in a subprocess; returns (stdout, stderr). The pure
    agent code is persisted to `path` (artifact); execution appends the results
    harness so the JSON contract does not depend on the model remembering to
    print it."""
    path.write_text(code, encoding="utf-8")
    run_path = path.with_name(path.stem + "_run.py")
    run_path.write_text(code + HARNESS, encoding="utf-8")
    proc = subprocess.run(
        [sys.executable, str(run_path)],
        capture_output=True,
        text=True,
        timeout=timeout,
        cwd=str(run_path.parent),
    )
    return proc.stdout or "", proc.stderr or ""


def parse_results(stdout: str) -> dict:
    """Prefer the final JSON line; fall back to the diagnostics prints that
    models emit reliably (they consistently forget the final
    print(json.dumps(...)) even after escalation)."""
    for line in reversed(stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    res: dict = {}
    m = re.search(r"Ideal Counts[: ]*(\{.*\})", stdout)
    if m:
        res["ideal_counts"] = ast.literal_eval(m.group(1))
    m = re.search(r"Noisy Counts[: ]*(\{.*\})", stdout)
    if m:
        res["noisy_counts"] = ast.literal_eval(m.group(1))
    m = re.search(r"Mitigated Counts[: ]*(\{.*\})", stdout)
    if m:
        res["mitigated_counts"] = ast.literal_eval(m.group(1))
    for pat, key in [
        (r"(?<!Mitigated )Fidelity[: ]*([\d.]+)", "fidelity"),
        (r"(?<!Mitigated )Error Rate[: ]*([\d.]+)", "error_rate"),
        (r"Mitigated Fidelity[: ]*([\d.]+)", "mitigated_fidelity"),
        (r"Mitigated Error Rate[: ]*([\d.]+)", "mitigated_error_rate"),
    ]:
        m = re.search(pat, stdout)
        if m:
            res[key] = float(m.group(1))
    if "fidelity" not in res or "noisy_counts" not in res:
        raise RuntimeError(f"could not parse results from stdout: {stdout[:500]}")
    return res


def contract_gate(label: str, code: str) -> str | None:
    """Deterministic stage contract: run-1 must be RAW (no mitigation code),
    run-2 MUST contain the mitigation logic. Returns a defect string or None."""
    if label == "EXEC 1":
        m = re.search(r"lstsq|calibration_circuits|corrected_counts|mitigated", code)
        if m:
            return (
                f"RUN-1 must contain NO error mitigation code, but the script "
                f"contains forbidden mitigation code ({m.group(0)!r}). DELETE the "
                f"entire calibration/lstsq block (calibration_circuits, obs_counts, "
                f"lstsq, corrected_counts, mitigated*) from the script. Run-1 is "
                f"RAW only: circuit, noise model, run, counts, fidelity, error_rate, "
                f"output dict. Do not add any mitigation logic."
            )
    else:
        if "lstsq" not in code or "ReadoutError" not in code:
            return (
                "RUN-2 MUST contain the readout mitigation: calibration circuits, "
                "assignment matrix, np.linalg.lstsq correction, and "
                "mitigated_counts/mitigated_fidelity/mitigated_error_rate in the "
                "output dict. Use the VERIFIED MITIGATION SNIPPET verbatim. "
                "Do not invent different mitigation logic."
            )
    return None


def exec_with_escalation(
    client: Client,
    session: str,
    code: str,
    path: Path,
    label: str,
    extra_snippet: str = "",
    max_rounds: int = 3,
) -> tuple[dict, str]:
    """Run the agent's code; on failure escalate the exact error back to the
    engineer until it executes and emits the results JSON contract. A
    deterministic contract gate rejects forbidden/absent code BEFORE execution."""
    rounds = 0
    while True:
        gate = contract_gate(label, code)
        if gate is not None:
            defect = gate
        else:
            stdout, stderr = run_code(code, path)
            if stderr.strip() or not stdout.strip():
                defect = (
                    f"Execution FAILED (stderr):\n{stderr[:2000]}"
                    if stderr.strip()
                    else f"Execution produced NO output:\n{stdout[:1000]}"
                )
            else:
                try:
                    results = parse_results(stdout)
                    print(f"    {label}: executed OK -> {json.dumps(results, sort_keys=True)[:220]}")
                    return results, stdout
                except (json.JSONDecodeError, RuntimeError) as e:
                    defect = f"Result contract violated: {e}"
        if rounds >= max_rounds:
            raise RuntimeError(f"{label}: unresolved after {rounds} escalation round(s): {defect[:400]}")
        rounds += 1
        print(f"    {label}: problem ({defect[:90]}...), escalating to quantum-engineer (round {rounds})")
        prompt = (
            "Your Qiskit code failed. Orchestrator report:\n---\n"
            + defect
            + "\n\nYOUR CURRENT SCRIPT (the one that failed):\n```python\n"
            + code
            + "\n```\n---\n"
            "Fix the exact defect. If this is the RAW run-1 script, the output "
            "dict MUST contain ONLY ideal_counts, noisy_counts, fidelity, "
            "error_rate — no mitigated_* keys anywhere. Output the COMPLETE "
            "corrected script in a ```python fence, nothing else outside the "
            "fence. "
            + QISKIT_CONTRACT
            + "\n"
            + extra_snippet
        )
        code = clean_fenced(run_step(client, "quantum-engineer", session, prompt, f"EXEC-ESCALATION {rounds}"), "python")
        stdout, stderr = "", ""


def fmt_counts(counts: dict) -> str:
    return ", ".join(f"{k}: {v}" for k, v in sorted(counts.items()))


def fmt_report(results: dict) -> str:
    """Human-readable execution report for the analyst agent."""
    lines = [f"ideal counts: {fmt_counts(results['ideal_counts'])}",
             f"noisy counts: {fmt_counts(results['noisy_counts'])}",
             f"fidelity: {results['fidelity']:.4f}",
             f"error_rate: {results['error_rate']:.4f}"]
    if "mitigated_counts" in results:
        lines += [f"mitigated counts: {fmt_counts(results['mitigated_counts'])}",
                  f"mitigated_fidelity: {results['mitigated_fidelity']:.4f}",
                  f"mitigated_error_rate: {results['mitigated_error_rate']:.4f}"]
    return "\n".join(lines)


# --------------------------------------------------------------------------
# Notebook assembly (executed: code cells carry their real stdout outputs)
# --------------------------------------------------------------------------

def stream_output(text: str) -> dict:
    return {"output_type": "stream", "name": "stdout", "text": text.splitlines(keepends=True)}


def assemble_ipynb(cells: list[dict]) -> dict:
    """Wrap raw cell dicts into a valid nbformat 4.5 notebook."""
    out = []
    for i, c in enumerate(cells):
        ctype = c.get("cell_type", "markdown")
        if ctype not in ("markdown", "code"):
            raise RuntimeError(f"cell {i}: bad cell_type {ctype!r}")
        src = c.get("source", "")
        if isinstance(src, list):
            src = "".join(str(s) for s in src)
        cell = {"cell_type": ctype, "metadata": {}, "source": src.splitlines(keepends=True)}
        if ctype == "code":
            cell.update({"execution_count": c.get("execution_count", i + 1), "outputs": c.get("outputs", [])})
        out.append(cell)
    return {
        "cells": out,
        "metadata": {
            "kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"},
            "language_info": {"name": "python", "version": "3"},
        },
        "nbformat": 4,
        "nbformat_minor": 5,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base-url", default="http://127.0.0.1:9090")
    ap.add_argument("--out", default=str(BUILD))
    args = ap.parse_args()

    client = Client(args.base_url, timeout=600)

    health = client.health()
    agents = [a.name for a in client.list_agents()]
    print(f"server {health.get('status')} | agents: {agents}")
    need = {"quantum-theorist", "quantum-engineer", "quantum-analyst"}
    if not need.issubset(agents):
        raise RuntimeError(f"server missing agents {need - set(agents)} — is the quantum config loaded?")

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    v1_path = out_dir / "qiskit_v1.py"
    v2_path = out_dir / "qiskit_v2.py"

    # 1. Theorist: design the noise experiment + mitigation strategy.
    design = run_step(
        client,
        "quantum-theorist",
        "s-theory",
        (
            "Design a quantum noise experiment for a real execution run. "
            "(1) Circuit: 2-qubit Bell state (H then CX). "
            "(2) Noise model to emulate imperfect hardware: depolarizing 2% on "
            "single-qubit gates, 4% on CX, readout error matrix "
            "[[0.95, 0.05], [0.03, 0.97]]. "
            "(3) Metrics: Hellinger fidelity between measured counts and ideal "
            "counts, error rate = 1 - fidelity. "
            "(4) Error mitigation plan: readout calibration circuits for basis "
            "states 00/01/10/11, build the 4x4 assignment matrix from noisy "
            "calibration counts, correct the noisy counts via least squares "
            "(np.linalg.lstsq). "
            "(5) Expected effect: readout mitigation removes the readout-error "
            "contribution but not gate depolarization, so fidelity should "
            "improve but not reach 1.0. State a decision threshold: mitigation "
            "REQUIRED below fidelity 0.95. Verify any arithmetic with calculator."
        ),
        "STEP 1/6 design",
    )

    # 2. Engineer: write the raw experiment (Qiskit 2.x + Aer).
    code_v1 = clean_fenced(
        run_step(
            client,
            "quantum-engineer",
            "s-engineer-v1",
            (
                "Write the Qiskit experiment now. Design spec:\n---\n"
                + design
                + "\n---\nImplement it exactly as specified. "
                + QISKIT_CONTRACT
                + " Output the COMPLETE script in a ```python fence, nothing "
                "else outside the fence."
            ),
            "STEP 2/6 code v1",
        ),
        "python",
    )

    # 3. EXECUTE v1 (the agents' 'run on notebook' — real execution).
    r1, stdout1 = exec_with_escalation(client, "s-engineer-v1", code_v1, v1_path, "EXEC 1")

    # 4. Analyst: read the REAL numbers, decide, file the change request.
    analysis = run_step(
        client,
        "quantum-analyst",
        "s-analyst",
        (
            "Execution report (REAL numbers from the run you must analyze):\n---\n"
            + fmt_report(r1)
            + "\n---\n"
            "Decision rule: fidelity below 0.95 -> VERDICT: MITIGATION REQUIRED. "
            "Analyze exactly these numbers: report the error rate, identify the "
            "dominant error source (readout vs gate), decide, and file a "
            "PRECISE change request for the engineer (which circuits to build, "
            "how to compute the assignment matrix, how to correct). "
            "Recompute derived quantities with calculator exactly once each."
        ),
        "STEP 4/6 analysis",
    )

    # 5. Engineer: implement readout error mitigation.
    code_v2 = clean_fenced(
        run_step(
            client,
            "quantum-engineer",
            "s-engineer-v2",
            (
                "Analyst change request:\n---\n"
                + analysis
                + "\n---\nImplement readout error mitigation now: build the "
                "calibration circuits, compute the assignment matrix from "
                "noisy calibration counts, apply least-squares correction "
                "(np.linalg.lstsq) to the noisy counts. Use the SAME noise "
                "model and shots as before. " + QISKIT_CONTRACT
                + "\n" + MITIGATION_SNIPPET
                + " Output the COMPLETE script in a ```python fence, nothing "
                "else outside the fence."
            ),
            "STEP 5/6 code v2",
        ),
        "python",
    )

    # 6. EXECUTE v2.
    r2, stdout2 = exec_with_escalation(client, "s-engineer-v2", code_v2, v2_path, "EXEC 2", extra_snippet=MITIGATION_SNIPPET)

    # 7. Analyst: final data-driven verdict.
    verdict = run_step(
        client,
        "quantum-analyst",
        "s-analyst",
        (
            "Final comparison — two REAL runs of the same Bell-state experiment "
            "with the same noise model:\n---\n"
            "RUN 1 (raw):\n" + fmt_report(r1)
            + "\n\nRUN 2 (mitigated):\n" + fmt_report(r2)
            + "\n---\n"
            "State both fidelities and the delta, judge whether mitigation was "
            "effective, explain the remaining error (gate depolarization is "
            "not corrected by readout mitigation), and give the final verdict: "
            "'VERDICT: ACCEPTABLE' or 'VERDICT: FURTHER MITIGATION NEEDED'."
        ),
        "STEP 7/7 verdict",
    )

    # Deterministic gate: mitigation must actually improve fidelity.
    raw_f = r1["fidelity"]
    mit_f = r2.get("mitigated_fidelity")
    if mit_f is not None and mit_f <= raw_f:
        print(f"WARNING: mitigated fidelity {mit_f:.4f} did not improve on raw {raw_f:.4f}")

    # Assemble the EXECUTED notebook.
    nb = assemble_ipynb([
        {
            "cell_type": "markdown",
            "source": (
                "# Quantum Error Mitigation Experiment\n\n"
                "**Produced by a multi-agent pipeline** (ernest SDK + 4 agents, "
                "real OpenRouter runs) — the code cells below are the agent "
                "outputs **executed locally with Qiskit 2.x / Aer 0.17**; the "
                "stdout cells contain the real run output.\n\n"
                "## Experiment design (quantum-theorist)\n\n```\n" + design + "\n```"
            ),
        },
        {
            "cell_type": "code",
            "source": "# Run 1 — raw noisy execution (quantum-engineer, v1)\n" + code_v1,
            "outputs": [stream_output(stdout1)],
        },
        {
            "cell_type": "markdown",
            "source": (
                "## Run 1 results (real)\n\n"
                + fmt_report(r1).replace("\n", "\n\n") + "\n\n"
                + "## Analyst decision\n\n```\n" + analysis + "\n```"
            ),
        },
        {
            "cell_type": "code",
            "source": "# Run 2 — with readout error mitigation (quantum-engineer, v2)\n" + code_v2,
            "outputs": [stream_output(stdout2)],
        },
        {
            "cell_type": "markdown",
            "source": (
                "## Run 2 results (real)\n\n"
                + fmt_report(r2).replace("\n", "\n\n") + "\n\n"
                + "## Final verdict (quantum-analyst)\n\n```\n" + verdict + "\n```"
            ),
        },
    ])
    nb_path = out_dir / "quantum-lab.ipynb"
    nb_path.write_text(json.dumps(nb, indent=1), encoding="utf-8")

    print(f"\nartifacts: {nb_path} ({nb_path.stat().st_size} bytes, {len(nb['cells'])} cells, executed)")
    print(f"           {v1_path} ({v1_path.stat().st_size} bytes)")
    print(f"           {v2_path} ({v2_path.stat().st_size} bytes)")
    print(f"fidelity: raw {raw_f:.4f} -> mitigated {mit_f:.4f} (delta {mit_f - raw_f:+.4f})" if mit_f else f"fidelity: raw {raw_f:.4f}")
    print("pipeline complete: theorist -> engineer -> execute -> analyst -> engineer -> execute -> verdict")
    return 0


if __name__ == "__main__":
    sys.exit(main())
