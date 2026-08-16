"""ernest authoring DSL — agents, teams, tasks and crews as Python.

The DSL compiles to the exact ``ernest.json`` schema the Go core loads
(agents / teams / workflows), so a Python-authored crew runs on the same
engine as a hand-written config file — delegation, guards, retries and
streaming are the Go engine's, not a Python reimplementation.

Usage::

    from ernest.dsl import Agent, Crew, Task

    researcher = Agent("researcher", instructions="Find facts and report them.")
    writer = Agent("writer", instructions="Turn notes into prose.")
    crew = Crew("pipeline", tasks=[
        Task(researcher, "Research: {{input}}", name="research"),
        Task(writer, "Write a summary from: {{research}}", name="write"),
    ])
    crew.to_config()  # dict ready for ernest.json

Serialization is validated end-to-end: ``pytest`` feeds ``to_config()``
output through ``ernest doctor`` (the Go validator) and runs crews with
the real binary via :mod:`ernest.runner`.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional, Union

VALID_PROCESSES = ("hierarchical", "sequential")

AgentRef = Union["Agent", str]


def _agent_name(ref: AgentRef) -> str:
    if isinstance(ref, Agent):
        return ref.name
    if isinstance(ref, str) and ref:
        return ref
    raise ValueError(f"agent reference must be an Agent or a name, got {ref!r}")


@dataclass
class Agent:
    """One agent as it appears in the ``agents`` array of ernest.json.

    Args:
        name: unique agent name (referenced by teams/tasks).
        provider: "mock" (default, keyless), "openai", "compatible"
            (any OpenAI-compatible API, e.g. OpenRouter) or "ollama".
        model: model id; defaults to the provider's demo default.
        base_url: API endpoint override (``baseUrl``), e.g.
            ``https://openrouter.ai/api/v1`` for the "compatible"
            provider; keyless providers can leave it unset.
        api_key_env: env var holding the API key (``apiKeyEnv``),
            e.g. ``OPENROUTER_API_KEY``.
        instructions: system instructions.
        tools: built-in tool names (calculator, http_fetch, now, ...).
        tool_sandbox: base directory for the sandboxed file/shell tools
            (required by the Go validator when those tools are attached).
        tool_policy: dict passed through verbatim in the config's
            ``toolPolicy`` shape (``enableShell``, ``autoApprove``).
        memory: persist sessions (requires a session store).
        knowledge: optional dict in the config's ``knowledge`` shape
            (see the ernest.json docs), passed through verbatim.
        max_iterations: cap on the agent's tool-use loop.
    """

    name: str
    provider: str = "mock"
    model: Optional[str] = None
    base_url: Optional[str] = None
    api_key_env: Optional[str] = None
    instructions: str = ""
    description: str = ""
    tools: List[str] = field(default_factory=list)
    tool_sandbox: Optional[str] = None
    tool_policy: Optional[Dict[str, Any]] = None
    memory: bool = True
    knowledge: Optional[Dict[str, Any]] = None
    max_iterations: Optional[int] = None

    def to_config(self) -> Dict[str, Any]:
        if not self.name:
            raise ValueError("Agent name is required")
        provider = (self.provider or "mock").lower()
        if provider != "mock" and not self.model:
            # Mirrors the Go validator: non-mock providers need a model id.
            raise ValueError(
                f"agent {self.name!r}: model is required for provider {provider!r}"
            )
        cfg: Dict[str, Any] = {
            "name": self.name,
            "provider": provider,
            "model": self.model or ("mock-1" if provider == "mock" else ""),
        }
        if self.base_url:
            cfg["baseUrl"] = self.base_url
        if self.api_key_env:
            cfg["apiKeyEnv"] = self.api_key_env
        if self.description:
            cfg["description"] = self.description
        if self.instructions:
            cfg["instructions"] = self.instructions
        if self.tools:
            cfg["tools"] = list(self.tools)
        if self.tool_sandbox:
            cfg["toolSandbox"] = self.tool_sandbox
        if self.tool_policy is not None:
            cfg["toolPolicy"] = dict(self.tool_policy)
        if not self.memory:
            cfg["memory"] = False
        if self.knowledge is not None:
            cfg["knowledge"] = dict(self.knowledge)
        if self.max_iterations is not None:
            cfg["maxIterations"] = self.max_iterations
        return cfg


@dataclass
class Guard:
    """LLM-judged quality gate on a workflow step output.

    The step's agent provider scores the output against ``rubric``
    (0.0..1.0); the step fails below ``min_score`` (default 0.7).
    Requires a real (or scripted) model — the mock provider cannot judge.
    """

    rubric: str
    min_score: float = 0.7

    def to_config(self) -> Dict[str, Any]:
        if not self.rubric:
            raise ValueError("Guard rubric is required")
        cfg: Dict[str, Any] = {"rubric": self.rubric}
        if self.min_score:
            cfg["minScore"] = self.min_score
        return cfg


@dataclass
class Task:
    """One workflow step: a prompt run through an agent.

    Args:
        agent: the :class:`Agent` (or its name) that runs this step.
        prompt: prompt for the step; ``{{name}}`` / ``{{input}}``
            placeholders are replaced with earlier step outputs.
        name: step name (defaults to a slug derived from the agent).
        guard: optional :class:`Guard` judged after the step output.
        depends_on: tasks (or step names) that must finish first.
            When omitted, tasks in a :class:`Crew` chain in order.
        retries: retry count on retryable failures.
    """

    agent: AgentRef
    prompt: str
    name: Optional[str] = None
    guard: Optional[Guard] = None
    depends_on: List[Union["Task", str]] = field(default_factory=list)
    retries: int = 0

    def step_name(self) -> str:
        if self.name:
            return self.name
        return _agent_name(self.agent)

    def to_config(self) -> Dict[str, Any]:
        cfg: Dict[str, Any] = {
            "name": self.step_name(),
            "agent": _agent_name(self.agent),
            "prompt": self.prompt,
        }
        if self.guard is not None:
            cfg["guard"] = self.guard.to_config()
        deps = [_dep_name(d) for d in self.depends_on]
        if deps:
            cfg["dependsOn"] = deps
        if self.retries:
            cfg["retries"] = self.retries
        return cfg


def _dep_name(dep: Union["Task", str]) -> str:
    return dep.step_name() if isinstance(dep, Task) else dep


@dataclass
class Team:
    """A declarative team (the ``teams`` array of ernest.json).

    ``process="hierarchical"`` (default): the leader delegates to
    members through the injected ``delegate`` tool. ``"sequential"``:
    members run in order, each output feeding the next (no leader call).
    """

    leader: AgentRef
    members: List[AgentRef]
    name: str = ""
    description: str = ""
    process: str = "hierarchical"
    instructions: str = ""
    max_iterations: Optional[int] = None

    def to_config(self) -> Dict[str, Any]:
        process = (self.process or "hierarchical").lower()
        if process not in VALID_PROCESSES:
            raise ValueError(f"team {self.name!r}: unknown process {self.process!r} (hierarchical|sequential)")
        if not self.members:
            raise ValueError(f"team {self.name!r}: at least one member is required")
        cfg: Dict[str, Any] = {
            "name": self.name,
            "leader": _agent_name(self.leader),
            "members": [_agent_name(m) for m in self.members],
            "process": process,
        }
        if self.description:
            cfg["description"] = self.description
        if self.instructions:
            cfg["instructions"] = self.instructions
        if self.max_iterations is not None:
            cfg["maxIterations"] = self.max_iterations
        return cfg

    def agent_refs(self) -> List[Agent]:
        refs: List[Agent] = []
        if isinstance(self.leader, Agent):
            refs.append(self.leader)
        for m in self.members:
            if isinstance(m, Agent):
                refs.append(m)
        return refs


@dataclass
class Crew:
    """A named crew: agents + optional teams + optional task workflow.

    ``tasks`` compile into a declarative workflow named after the crew
    (steps chain in declaration order unless a task declares
    ``depends_on``) — the crew runs on the Go workflow engine, guards and
    retries included. ``teams`` compile into config teams runnable via
    ``ernest run --team`` or ``POST /api/teams/{name}/run``.
    """

    name: str
    agents: List[Agent] = field(default_factory=list)
    teams: List[Team] = field(default_factory=list)
    tasks: List[Task] = field(default_factory=list)

    def collect_agents(self) -> Dict[str, Agent]:
        by_name: Dict[str, Agent] = {}
        for a in self.agents:
            if a.name in by_name:
                raise ValueError(f"duplicate agent {a.name!r}")
            by_name[a.name] = a
        for t in self.teams:
            for a in t.agent_refs():
                by_name.setdefault(a.name, a)
        for task in self.tasks:
            if isinstance(task.agent, Agent):
                by_name.setdefault(task.agent.name, task.agent)
        return by_name

    def to_config(self) -> Dict[str, Any]:
        if not self.name:
            raise ValueError("Crew name is required")
        agents = self.collect_agents()
        if not agents:
            raise ValueError("crew has no agents (add agents, teams or tasks)")

        agent_cfgs = [a.to_config() for a in agents.values()]

        team_cfgs: List[Dict[str, Any]] = []
        team_names: Dict[str, bool] = {}
        for t in self.teams:
            if not t.name:
                raise ValueError("Team name is required in a crew (set Team.name)")
            if t.name in team_names:
                raise ValueError(f"duplicate team {t.name!r}")
            team_names[t.name] = True
            tcfg = t.to_config()
            if tcfg["leader"] not in agents:
                raise ValueError(f"team {t.name!r}: unknown leader {tcfg['leader']!r}")
            for m in tcfg["members"]:
                if m not in agents:
                    raise ValueError(f"team {t.name!r}: unknown member {m!r}")
            team_cfgs.append(tcfg)

        workflow_cfgs: List[Dict[str, Any]] = []
        if self.tasks:
            step_cfgs: List[Dict[str, Any]] = []
            step_names: Dict[str, bool] = {}
            explicit_deps = {
                _dep_name(d) for task in self.tasks for d in task.depends_on
            }
            chained = not explicit_deps  # only auto-chain when no explicit edges
            prev: Optional[str] = None
            for task in self.tasks:
                cfg = task.to_config()
                if cfg["name"] in step_names:
                    raise ValueError(f"duplicate task/step {cfg['name']!r} (set Task.name)")
                step_names[cfg["name"]] = True
                if cfg["agent"] not in agents:
                    raise ValueError(f"task {cfg['name']!r}: unknown agent {cfg['agent']!r}")
                if chained and prev is not None and prev not in cfg.get("dependsOn", []):
                    cfg["dependsOn"] = [prev] + cfg.get("dependsOn", [])
                for dep in cfg.get("dependsOn", []):
                    if dep not in step_names and not any(t.step_name() == dep for t in self.tasks):
                        raise ValueError(f"task {cfg['name']!r} depends on unknown step {dep!r}")
                step_cfgs.append(cfg)
                prev = cfg["name"]
            _raise_on_cycle(step_cfgs)
            workflow_cfgs.append({"name": self.name, "steps": step_cfgs})

        cfg: Dict[str, Any] = {
            "agents": agent_cfgs,
            "store": {"type": "memory"},
        }
        if team_cfgs:
            cfg["teams"] = team_cfgs
        if workflow_cfgs:
            cfg["workflows"] = workflow_cfgs
        return cfg


def _raise_on_cycle(steps: List[Dict[str, Any]]) -> None:
    """Kahn's check over the compiled step list (fail fast in Python)."""
    indeg = {s["name"]: len(s.get("dependsOn", [])) for s in steps}
    dependents: Dict[str, List[str]] = {}
    for s in steps:
        for d in s.get("dependsOn", []):
            dependents.setdefault(d, []).append(s["name"])
    queue = [n for n, deg in indeg.items() if deg == 0]
    processed = 0
    while queue:
        n = queue.pop(0)
        processed += 1
        for nxt in dependents.get(n, []):
            indeg[nxt] -= 1
            if indeg[nxt] == 0:
                queue.append(nxt)
    if processed != len(steps):
        raise ValueError("task graph contains a dependency cycle")
