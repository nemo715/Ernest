# ernest vs CrewAI

An honest, dated comparison. "Wins" means real, shipped capabilities —
not marketing. Anything not independently measured is marked as such.

*Updated: 2026-08*

## TL;DR

CrewAI is the Python ecosystem's standard for multi-agent crews with an
enterprise platform around it. ernest is a Go single-binary runtime that
targets the same authoring ergonomics (crews, teams, workflows, Python
DSL) with a different deployment story. Both get a real model from A to
B in the same wall-clock time; the differences are ecosystem,
operational surface, and depth of tooling.

## Side by side

| | ernest | CrewAI |
|---|---|---|
| Language | Go core + Python SDK/DSL | Python |
| Authoring | `ernest.json` (declarative) or Python DSL (`Agent`/`Task`/`Crew`) | Python (`Agent`, `Task`, `Crew`, `Flow`) |
| Orchestration | Teams (hierarchical/sequential), workflows (DAG steps, guards, retries) — config **or** Python | Crews (sequential/hierarchical processes), Flows (event-driven), @-decorator routes |
| LLM providers | OpenAI-compatible endpoint (OpenRouter, Groq, Ollama, vLLM…) + mock | ~everything via litellm + custom LLM classes |
| Tools | Built-ins (calculator, http_fetch, web_search, sandboxed file/shell, browser) + MCP + custom tools (Go/Python) | LangChain tools + custom @tool + MCP |
| Human-in-the-loop | First-class: approval events stream to UI/SDK; deny is enforced | `human_input` flag + human feedback modes |
| Streaming | Token deltas, tool calls, delegation, metrics over SSE **and** WebSocket; interrupt/steer | AMP dashboard / callbacks (mostly server-side batching) |
| Observability | Built-in traces (exact model context), audit log, failures feed, eval suite, trace ingestion from any framework | AMP/self-hosted platform, Langfuse/Phoenix integrations |
| Deployment | One static binary (~20 MB) serving API + UI; no Python runtime needed | Python service + platform (cloud/enterprise) or self-host |
| Evals | `ernest eval` (assertions + LLM judge + baselines), CI gate, learn-from-failures | Custom eval harnesses / platform features |
| Ecosystem | Small; MCP + A2A for interop | Large: thousands of integrations, docs, community |
| Cold start (measured) | ~17–19 ms | ~235 ms (langchain import chain; platform adds more) |

## Where CrewAI wins (real)

- **Ecosystem & community**: docs, templates, integrations, answers to
  every Stack Overflow question. ernest will not catch this soon.
- **Provider breadth**: litellm covers more model vendors than a single
  OpenAI-compatible endpoint.
- **Enterprise platform**: hosted orchestration, AMP observability,
  training, studio UI, support — ernest has a local dev console, not a
  SaaS.
- **Python-only shops**: no Go toolchain to install; agents live in the
  same language as the rest of the data stack.
- **Flows**: event-driven, fine-grained control flow with explicit state
  and @start/@listen routing is more expressive than a declarative DAG.

## Where ernest wins (real)

- **Operational surface**: one static binary, no Python runtime, no venv,
  no dependency tree. `doctor` validates a deployment in one command.
- **Speed of the framework itself** (measured, [TEST.md](../TEST.md)):
  ~0.2% per-turn overhead vs the network-bound model latency that
  dominates both frameworks. Import-to-ready ~18 ms vs ~235 ms.
- **Streaming as the contract**: every event (token delta, tool call,
  approval, delegation, metrics) streams over SSE *and* WebSocket by
  default — clients render live progress without polling.
- **Deterministic mock provider**: entire crews/workflows/teams run
  scripted, keyless, offline — demos, CI and eval gates run anywhere.
- **HITL enforcement**: denied tools are structurally guaranteed not to
  execute (approval is a hard gate in the run loop), approval spans are
  audit-logged with the exact arguments.
- **Config-driven orchestration**: teams/workflows in `ernest.json`,
  runnable from CLI or HTTP without writing code — and the same shapes
  are authorable in Python when you prefer it.
- **Multi-language consumers**: one wire format for Go, Python, the web
  console, MCP and A2A.

## What ernest still lags (honest)

- **Memory/vector performance and scale** are not benchmarked (marked
  unmeasured in [TEST.md](../TEST.md)); CrewAI has years of production
  mileage there.
- **No SaaS**: observability, multi-tenant orchestration and auth are
  DIY (Postgres backend exists but is not wired into config validation
  yet).
- **Fewer built-in integrations**: anything beyond the built-ins goes
  through MCP — which is an escape hatch, not a catalog.
- **Guardrails** (token/cost caps, deny-lists, redaction) are Go API
  fields today, not `ernest.json` fields.

## Bottom line

Choose **CrewAI** if you live in the Python ecosystem, need the
platform, or want the deepest integration catalog. Choose **ernest** if
you want a single-binary runtime with first-class streaming, strict HITL
enforcement, deterministic keyless testing, and orchestration that runs
identically from config, Python, Go or HTTP.
