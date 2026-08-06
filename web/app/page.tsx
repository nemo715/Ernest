"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import {
  Badge,
  Card,
  PageHead,
  SkeletonRows,
  StatusBadge,
} from "@/components/ui";
import { getAgents, getAudit, getRuns, listSessions } from "@/lib/api";
import type { AgentInfo, AuditEntry, RunSummary, SessionInfo } from "@/lib/types";
import { fmtTime } from "@/lib/format";
import { RunTable } from "@/components/RunTable";

export default function OverviewPage() {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null);
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null);
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [audit, setAudit] = useState<AuditEntry[] | null>(null);

  useEffect(() => {
    let alive = true;
    Promise.all([getAgents(), listSessions(), getRuns(), getAudit(20)])
      .then(([a, s, r, au]) => {
        if (!alive) return;
        setAgents(a);
        setSessions(s);
        setRuns(r.runs);
        setAudit(au);
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  const failed = runs?.filter((r) => r.status === "failed").length ?? null;
  const pending =
    sessions?.reduce((n, s) => n + (s.pendingApprovals ?? 0), 0) ?? null;

  return (
    <Shell>
      <PageHead
        title="Overview"
        desc="A live view of the ernest runtime: agents, sessions, runs and the audit trail."
      />

      <div className="stat-grid">
        <Stat label="Agents" value={agents == null ? "…" : agents.length} sub={agents?.map((a) => a.name).join(", ") || "—"} />
        <Stat label="Sessions" value={sessions == null ? "…" : sessions.length} sub="durable, resumable" />
        <Stat label="Runs" value={runs == null ? "…" : runs.length} sub={`${failed ?? "…"} failed`} />
        <Stat label="Pending approvals" value={pending == null ? "…" : pending} sub="human-in-the-loop" />
      </div>

      <Card
        title="Recent runs"
        desc="Every traced run, newest first — spans, metrics and the assembled context live under each run."
        actions={
          <Link href="/runs" className="btn btn-sm">
            all runs
          </Link>
        }
      >
        {runs == null ? (
          <SkeletonRows rows={4} />
        ) : runs.length === 0 ? (
          <div className="muted small" style={{ padding: "8px 0" }}>
            No runs yet. Start one from the{" "}
            <Link href="/playground">playground</Link>.
          </div>
        ) : (
          <RunTable runs={runs.slice(0, 6)} />
        )}
      </Card>

      <Card
        title="Audit trail"
        desc="Tool calls, approvals and run outcomes — every side effect, timestamped."
        actions={
          <Link href="/audit" className="btn btn-sm">
            full log
          </Link>
        }
      >
        {audit == null ? (
          <SkeletonRows rows={3} />
        ) : audit.length === 0 ? (
          <div className="muted small">Nothing recorded yet.</div>
        ) : (
          <div className="mono small" style={{ fontSize: 11.5 }}>
            {audit.slice(0, 5).map((e) => (
              <div key={e.id} className="row" style={{ padding: "4px 0" }}>
                <span className="faint tnum">{fmtTime(e.time)}</span>
                <Badge tone="muted">{e.kind}</Badge>
                <span className="muted">{e.agent}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </Shell>
  );
}

function Stat({
  label,
  value,
  sub,
}: {
  label: string;
  value: string | number;
  sub?: string;
}) {
  return (
    <div className="stat">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
    </div>
  );
}
