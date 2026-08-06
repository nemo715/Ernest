"use client";

import { useEffect, useState } from "react";
import { Shell } from "@/components/Shell";
import { Badge, Card, PageHead, SkeletonRows } from "@/components/ui";
import { getAgents } from "@/lib/api";
import type { AgentInfo } from "@/lib/types";

export default function AgentsPage() {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getAgents()
      .then(setAgents)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  return (
    <Shell>
      <PageHead
        title="Agents"
        desc="The agents served by this runtime. Each agent is also reachable as an MCP tool (ernest mcp-serve) and over A2A."
      />
      {error && (
        <div className="notice danger" style={{ marginBottom: 12 }}>
          {error}
        </div>
      )}
      {agents == null ? (
        <SkeletonRows rows={3} />
      ) : agents.length === 0 ? (
        <Card>
          <div className="muted">No agents configured.</div>
        </Card>
      ) : (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))", gap: 12 }}>
          {agents.map((a) => (
            <Card
              key={a.name}
              title={
                <span className="row">
                  <span style={{ fontWeight: 650 }}>{a.name}</span>
                  <Badge tone="accent">{a.provider}</Badge>
                </span>
              }
              desc={a.description || "No description."}
            >
              <div className="row wrap" style={{ marginBottom: 8 }}>
                <span className="mono small faint">{a.model}</span>
                {a.tools.map((t) => (
                  <Badge key={t}>{t}</Badge>
                ))}
                {a.tools.length === 0 && <span className="faint small">no tools</span>}
              </div>
              <div className="row">
                <Badge tone="muted">A2A</Badge>
                <Badge tone="muted">MCP tool</Badge>
                <Badge tone="muted">SSE / WS</Badge>
              </div>
            </Card>
          ))}
        </div>
      )}
    </Shell>
  );
}
