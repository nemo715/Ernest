"use client";

// Audit log — every significant event the runtime recorded: tool calls,
// approval decisions, run outcomes. The same feed the server keeps in
// memory (GET /api/audit).

import { useEffect, useState } from "react";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import { Badge, Button, CodeBlock, Empty, PageHead, SkeletonRows } from "@/components/ui";
import { getAudit } from "@/lib/api";
import type { AuditEntry } from "@/lib/types";
import { fmtClock, shortID } from "@/lib/format";

function auditTone(kind: string): "ok" | "warn" | "danger" | "accent" | "muted" {
  if (kind.includes("fail") || kind.includes("interrupt") || kind.includes("reject")) return "danger";
  if (kind.includes("approval")) return "warn";
  if (kind.includes("complete")) return "ok";
  if (kind.includes("tool.call")) return "accent";
  return "muted";
}

export default function AuditPage() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    setError(null);
    getAudit(100)
      .then(setEntries)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [tick]);

  return (
    <Shell>
      <PageHead
        title="Audit log"
        desc="Everything worth knowing about what the agents did, in order. Tool calls, approval decisions, run outcomes — with the raw detail one click away."
        actions={
          <Button variant="ghost" size="sm" onClick={() => setTick((t) => t + 1)}>
            refresh
          </Button>
        }
      />

      {error && (
        <div className="notice danger" style={{ marginBottom: 12 }}>
          {error}
        </div>
      )}

      {entries == null ? (
        <SkeletonRows rows={6} />
      ) : entries.length === 0 ? (
        <Empty
          title="Audit is empty"
          hint="Tool calls, approvals and run outcomes appear here as they happen."
        />
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>Agent</th>
                <th>Run</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e, i) => (
                <AuditRow key={e.id} entry={e} index={i} open={open === i} onToggle={() => setOpen(open === i ? null : i)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Shell>
  );
}

function AuditRow({
  entry,
  index,
  open,
  onToggle,
}: {
  entry: AuditEntry;
  index: number;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <>
      <tr>
        <td className="faint" style={{ whiteSpace: "nowrap" }}>{fmtClock(entry.time)}</td>
        <td>
          <Badge tone={auditTone(entry.kind)}>{entry.kind.replace(/\./g, " ")}</Badge>
        </td>
        <td>{entry.agent || "—"}</td>
        <td>
          {entry.runId ? (
            <Link href={`/runs/${entry.runId}`} className="link-cell mono">
              {shortID(entry.runId, 10)}
            </Link>
          ) : (
            <span className="faint">—</span>
          )}
        </td>
        <td style={{ textAlign: "right" }}>
          {entry.detail != null && (
            <button className="btn btn-ghost btn-sm" onClick={onToggle} aria-expanded={open}>
              {open ? "hide" : "detail"}
            </button>
          )}
        </td>
      </tr>
      {open && entry.detail != null && (
        <tr>
          <td colSpan={5}>
            <div style={{ padding: "2px 0 10px" }}>
              <CodeBlock value={entry.detail} />
            </div>
          </td>
        </tr>
      )}
    </>
  );
}
