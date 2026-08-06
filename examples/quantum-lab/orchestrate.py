"""quantum-lab orchestrator — multi-agent pipeline, ernest SDK only.

Three real agents (ernest.json) collaborate:
    quantum-theorist  -> design + grounded math (http_fetch Wikipedia, calculator)
    quantum-engineer  -> single-file HTML simulator + notebook cells
    quantum-reviewer  -> QA: recomputes probabilities with calculator, verdict

Pipeline: theorist -> engineer -> reviewer -> engineer(revision) -> artifacts,
plus a deterministic HITL escalation loop: the orchestrator statically checks
the HTML (canvas sizing, theta/phi math, joint normalization) and files the
exact defects back to the engineer until clean (max 3 rounds).

Artifacts written to build/:
    quantum-lab.html   self-contained interactive Bloch-sphere playground
    quantum-lab.ipynb  nbformat 4 notebook teaching the same math

Install:  pip install ernest
Run:      python orchestrate.py --base-url http://127.0.0.1:9293
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

from ernest import Client

BUILD = Path(__file__).resolve().parent / "build"

FENCE = re.compile(r"```(html|json)\s*\n(.*?)```", re.DOTALL)


def clean_fenced(text: str, lang: str) -> str:
    """Extract the first ```lang block, tolerating nested fences and trailing
    prose (models love to emit ```html```html ... ``` plus change-notes)."""
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
    # drop any stray opening fences nested at the very start
    lines = body.splitlines()
    while lines and lines[0].lstrip().startswith("```"):
        lines.pop(0)
    body = "\n".join(lines).strip()
    # cut trailing prose after the document terminator
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


def assemble_ipynb(cells: list[dict]) -> dict:
    """Wrap a raw cell array into a valid nbformat 4.5 notebook."""
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
            cell.update({"execution_count": None, "outputs": []})
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


def check_html(html: str) -> list[str]:
    """Deterministic static checks on the playground HTML.

    Learned from live verification rounds: the reviewer agent misses these
    classes of defects, so the orchestrator checks them mechanically and
    escalates the exact fix back to the engineer.
    """
    defects = []
    m = re.search(r"<canvas[^>]*>", html)
    if not m or "width=" not in m.group(0) or "height=" not in m.group(0):
        defects.append(
            'The <canvas> tag must declare width and height attributes (e.g. '
            'width="400" height="400") - without them the bitmap defaults to '
            '300x150 and the sphere is stretched into an ellipse.'
        )
    if "2 * Math.acos(alpha.magnitude())" not in html:
        defects.append(
            'In drawBlochSphere() the polar angle must be computed as '
            'theta = 2 * Math.acos(alpha.magnitude()) - using acos(alpha.real) '
            'or a missing factor of 2 collapses the state arrow for |0> or |1>.'
        )
    if "jointNorm" not in html:
        defects.append(
            'After applying a gate you must normalize alpha and beta JOINTLY: '
            'const jointNorm = Math.sqrt(alpha.magnitude()**2 + '
            'beta.magnitude()**2); then divide BOTH by it (skip if 0). '
            'Normalizing each component separately (alpha.normalize(); '
            'beta.normalize();) is wrong for superposition states and makes '
            'H|0> display P0=P1=1.00.'
        )
    drag_theta = re.search(r"Math\.acos\(Math\.max\(-1, Math\.min\(1, y / radius\)\)\)", html)
    if not drag_theta or "y / radius))) * 2" in html:
        defects.append(
            'In updateThetaPhi() the polar angle must be '
            'theta = Math.acos(Math.max(-1, Math.min(1, y / radius))) with NO '
            'factor of 2 (theta must stay within 0..PI so y=+R -> |0> and '
            'y=-R -> |1>).'
        )
    if "Math.atan2(y, x)" not in html:
        defects.append(
            'In updateThetaPhi() the azimuth must be phi = Math.atan2(y, x) '
            '(vertical offset y FIRST, horizontal offset x SECOND) so the state '
            'arrow follows the pointer.'
        )
    return defects


def escalation_prompt(defects: list[str]) -> str:
    body = "\n".join(f"{i + 1}. {d}" for i, d in enumerate(defects))
    return (
        "Orchestrator defect report on your final HTML. Verification found "
        f"{len(defects)} defect(s):\n---\n"
        + body
        + "\n---\nApply every defect exactly as described. Do NOT touch anything "
        "else (gate matrices, joint normalization, circuit tape, reset). "
        "Output the COMPLETE final single-file HTML in a ```html fence, nothing "
        "else outside the fence."
    )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base-url", default="http://127.0.0.1:9090")
    ap.add_argument("--out", default=str(BUILD))
    args = ap.parse_args()

    client = Client(args.base_url, timeout=600)

    health = client.health()
    agents = [a.name for a in client.list_agents()]
    print(f"server {health.get('status')} | agents: {agents}")
    need = {"quantum-theorist", "quantum-engineer", "quantum-reviewer"}
    if not need.issubset(agents):
        raise RuntimeError(f"server missing agents {need - set(agents)} — is the quantum config loaded?")

    # 1. Theorist: grounded design + exact math.
    design = run_step(
        client,
        "quantum-theorist",
        "s-theory",
        (
            "Design a single-file HTML quantum playground for ONE qubit. "
            "First use http_fetch on https://en.wikipedia.org/wiki/Bloch_sphere "
            "to ground the math. Then output: "
            "(1) the exact 2x2 matrices for X, Y, Z, H, S, T gates; "
            "(2) the mapping from Bloch angles theta/phi to alpha/beta, including "
            "the global-phase convention; "
            "(3) a numbered feature spec for a canvas-based playground: 3D-look "
            "sphere with state arrow, drag to set theta/phi, gate buttons that "
            "apply the matrices and re-render, live readout of alpha, beta and "
            "probabilities |alpha|^2, |beta|^2, a circuit tape, a reset button; "
            "(4) a gotchas list (normalization, canvas rotation order, "
            "complex multiplication). Verify any probability math with calculator."
        ),
        "STEP 1/5 theory",
    )

    # 2. Engineer: build the HTML.
    html_v1 = run_step(
        client,
        "quantum-engineer",
        "s-engineer",
        (
            "Build the quantum playground now. Design spec:\n---\n"
            + design
            + "\n---\nProduce a COMPLETE, WORKING single-file HTML in a ```html fence. "
            "Vanilla JS + inline CSS only, NO CDN, no external fonts. "
            "Render the sphere on a <canvas>, draw a state arrow (for |0> it must "
            "point UP at the north pole: theta = 2*acos(|alpha|), phi = atan2(beta.imag, "
            "beta.real)), redraw on every "
            "gate click, show alpha/beta/probabilities as text, keep a circuit tape "
            "of applied gates, drag on the sphere to set theta/phi, reset button. "
            "Verify |alpha|^2+|beta|^2 with calculator."
        ),
        "STEP 2/5 build",
    )

    # 3. Reviewer: QA with real calculator checks.
    review = run_step(
        client,
        "quantum-reviewer",
        "s-reviewer",
        (
            "Review this quantum playground HTML for correctness:\n---\n"
            + html_v1
            + "\n---\nUse calculator to recompute each check EXACTLY ONCE (never repeat the same "
            "expression; the run loop aborts on 3 identical calculator calls): H|0> gives "
            "P(0)=P(1)=0.5; "
            "X and Z matrices; normalization of the rendered statevector; "
            "check the JS for real bugs (complex multiplication, rotation order, "
            "NaN states). SPECIFIC RENDERING CHECKS: the state arrow must be visible "
            "for ANY pure state - for |0> it must point at the NORTH pole (up, full "
            "length), for |1> at the SOUTH pole (down); verify theta is computed as "
            "2*acos(|alpha|) (NOT acos(alpha.real), which collapses the arrow to zero "
            "length for the initial |0> state). Also verify the drag-to-set-theta/phi "
            "interaction from the spec is actually implemented. End with "
            "'VERDICT: APPROVE' or 'VERDICT: CHANGES REQUIRED' and a numbered defect list."
        ),
        "STEP 3/5 review",
    )

    # 4. Engineer: apply the review (same session -> sees its own HTML).
    html_final = run_step(
        client,
        "quantum-engineer",
        "s-engineer",
        (
            "Reviewer feedback on your HTML:\n---\n"
            + review
            + "\n---\nApply every concrete defect. Output the FINAL complete "
            "single-file HTML in a ```html fence, nothing else outside the fence."
        ),
        "STEP 4/5 revise",
    )

    # 5. Engineer: notebook cells (same session).
    cells_raw = run_step(
        client,
        "quantum-engineer",
        "s-engineer",
        (
            "Now write the companion notebook. Output a JSON array of nbformat 4 "
            "cells in a ```json fence (markdown + code, in teaching order): "
            "Bloch sphere intuition, the statevector math, gate matrices, and a "
            "tiny pure-Python single-qubit simulator (no libraries) with a worked "
            "H-gate example printing P(0)=0.5, P(1)=0.5. "
            'Cell shape: {"cell_type": "markdown"|"code", "source": "..."}. '
            "STRICT JSON: no raw backslashes (write |0>, sqrt(2), psi, alpha, beta, "
            "hbar in plain ASCII; Python code must not contain backslashes), "
            "double-quoted strings only, commas between all elements."
        ),
        "STEP 5/5 notebook",
    )

    # Assemble artifacts: HTML first, then a deterministic verification loop.
    # The reviewer agent (step 3) misses whole classes of defects, so the
    # orchestrator statically checks the artifact and escalates the exact
    # fixes back to the engineer (up to 3 rounds).
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    html_path = out_dir / "quantum-lab.html"
    html = clean_fenced(html_final, "html")
    html_path.write_text(html, encoding="utf-8")

    defects = check_html(html)
    rounds = 0
    while defects and rounds < 3:
        rounds += 1
        print(f"\n=== HITL escalation {rounds} -> quantum-engineer ({len(defects)} defect(s)) ===")
        for d in defects:
            print(f"    - {d[:100]}")
        fixed = run_step(
            client,
            "quantum-engineer",
            "s-engineer",
            escalation_prompt(defects),
            f"ESCALATION {rounds}",
        )
        html = clean_fenced(fixed, "html")
        html_path.write_text(html, encoding="utf-8")
        defects = check_html(html)
    if defects:
        print(f"WARNING: {len(defects)} defect(s) unresolved after {rounds} escalation round(s):")
        for d in defects:
            print(f"    - {d[:100]}")
    else:
        print("verification: all static checks passed")

    raw_cells = clean_fenced(cells_raw, "json")
    try:
        cells = json.loads(raw_cells)
    except json.JSONDecodeError:
        cells = json.loads(repair_json(raw_cells))
    if not isinstance(cells, list) or not cells:
        raise RuntimeError("notebook cells must be a non-empty JSON array")
    nb_path = out_dir / "quantum-lab.ipynb"
    nb_path.write_text(json.dumps(assemble_ipynb(cells), indent=1), encoding="utf-8")

    print(f"\nartifacts: {html_path} ({html_path.stat().st_size} bytes)")
    print(f"           {nb_path} ({nb_path.stat().st_size} bytes, {len(cells)} cells)")
    print("multi-agent pipeline complete: theorist -> engineer -> reviewer -> engineer")
    return 0


if __name__ == "__main__":
    sys.exit(main())
