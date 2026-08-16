// Static quick-reference docs served by the console itself. The full
// guides live in the repository (docs/GUIDE.md, docs/PYTHON.md, ...);
// this page covers the surfaces you can try against the running server.

import { Shell } from "@/components/Shell";
import { Badge, Card, PageHead } from "@/components/ui";

const QUICKSTART = `ernest init          # writes ernest.json (mock agent, no key)
ernest doctor        # validate config + connectivity
ernest run -input "6*7?"        # one-shot run
ernest playground --port 9090   # this console`;

const CONFIG = `{
  "agents": [
    {
      "name": "assistant",
      "provider": "compatible",
      "model": "openai/gpt-4o-mini",
      "baseUrl": "https://openrouter.ai/api/v1",
      "apiKeyEnv": "OPENROUTER_API_KEY",
      "instructions": "You are a helpful assistant.",
      "tools": ["calculator", "web_search", "file_read"],
      "toolSandbox": "sandbox"
    }
  ],
  "teams": [
    { "name": "editorial", "process": "sequential",
      "leader": "lead", "members": ["researcher", "writer"] }
  ],
  "workflows": [
    { "name": "pipeline", "steps": [
      { "name": "research", "agent": "researcher", "prompt": "Research {{input}}" },
      { "name": "write", "agent": "writer", "prompt": "Write from {{research}}",
        "dependsOn": ["research"] }
    ]}
  ]
}`;

const TEAM_RUN = `ernest run -team editorial -input "plan the release"
ernest run -workflow pipeline -input "quantum chips" --json

# same surfaces over HTTP (SSE):
#   GET  /api/teams            GET  /api/workflows
#   POST /api/teams/{name}/run POST /api/workflows/{name}/run`;

const PYTHON = `# crew.py — author in Python, run on the Go engine
from ernest import Agent, Task, Crew

researcher = Agent("researcher", provider="mock",
                   instructions="You research topics.")
writer = Agent("writer", provider="mock",
               instructions="You write clearly.")

crew = Crew("py-crew", tasks=[
    Task(researcher, "Research {{input}}", name="research"),
    Task(writer, "Write from {{research}}", name="write",
         depends_on=["research"]),
])

# python -m ernest doctor crew.py --json
# python -m ernest run crew.py --input "quantum chips" --json`;

const TOOLS: { tool: string; what: string; policy: string }[] = [
  { tool: "calculator / http_fetch / now", what: "Arithmetic, URL fetch, UTC time", policy: "runs freely" },
  { tool: "web_search", what: "DuckDuckGo HTML search (no API key)", policy: "runs freely" },
  { tool: "file_read / file_list", what: "Read / list inside the agent's toolSandbox", policy: "sandbox only" },
  { tool: "file_write", what: "Write / append inside the agent's toolSandbox", policy: "approval (autoApprove opt-out)" },
  { tool: "shell_exec", what: "Shell command inside the agent's toolSandbox", policy: "enableShell + always approval" },
  { tool: "browser_navigate / read / click / type / screenshot", what: "Drive a shared headless browser (CDP)", policy: "approval (autoApprove opt-out)" },
];

const ENDPOINTS: { path: string; what: string }[] = [
  { path: "POST /api/chat", what: "Streaming chat over SSE (message.delta, tool.call, approval.requested, ...)" },
  { path: "GET /ws/chat", what: "Same event stream over WebSocket (interrupt/steer)" },
  { path: "POST /api/approvals/{id}", what: "Approve / deny a pending tool call" },
  { path: "GET /api/teams · /api/workflows", what: "Config-driven orchestration registry" },
  { path: "GET /api/runs · /api/runs/{id}/trace", what: "Runs, traces and the exact model context" },
  { path: "GET /api/audit · /api/failures", what: "Append-only audit log, failure feed" },
  { path: "GET /api/agents · /api/sessions", what: "Agent registry, session store" },
];

export default function DocsPage() {
  return (
    <Shell>
      <PageHead
        title="Docs"
        desc="Quick reference. Full guides: docs/GUIDE.md · docs/PYTHON.md · docs/ARCHITECTURE.md · docs/COMPARISON.md in the repository."
      />

      <div style={{ display: "grid", gap: 12 }}>
        <Card title="Quickstart" desc="One static binary — no Python runtime required.">
          <pre className="code-block">{QUICKSTART}</pre>
        </Card>

        <Card title="ernest.json" desc="Agents, config-driven teams and workflow DAGs.">
          <pre className="code-block">{CONFIG}</pre>
        </Card>

        <Card title="Teams & workflows" desc="Sequential teams stream delegate.start/end per member; workflows stream step.start/end and run independent steps concurrently.">
          <pre className="code-block">{TEAM_RUN}</pre>
        </Card>

        <Card title="Built-in tools" desc="Attach per agent via tools: [...]; everything else via MCP (mcpServers).">
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr><th>Tool</th><th>What it does</th><th>Default policy</th></tr>
              </thead>
              <tbody>
                {TOOLS.map((t) => (
                  <tr key={t.tool}>
                    <td className="mono">{t.tool}</td>
                    <td>{t.what}</td>
                    <td>{t.policy}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>

        <Card title="Python" desc="Author crews in Python, run them on the Go engine; or drive this server with the SDK.">
          <pre className="code-block">{PYTHON}</pre>
        </Card>

        <Card title="HTTP API" desc="Everything the console renders is a public endpoint speaking the RunEvent wire format.">
          <div style={{ display: "grid", gap: 6 }}>
            {ENDPOINTS.map((e) => (
              <div key={e.path} className="row">
                <Badge tone="accent">{e.path}</Badge>
                <span className="muted">{e.what}</span>
              </div>
            ))}
          </div>
        </Card>
      </div>
    </Shell>
  );
}
