"use client";

// Approvals — every pending HITL request across sessions, in one queue.
// Decide here or from the session page; the two stay in sync.

import { useEffect, useState } from "react";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import {
  Badge,
  Button,
  Card,
  CodeBlock,
  Empty,
  PageHead,
  SkeletonRows,
} from "@/components/ui";
import { getSession, listSessions, streamApprove } from "@/lib/api";
import type { ApprovalRequest } from "@/lib/types";
import { fmtTime, shortID } from "@/lib/format";

interface PendingItem {
  approval: ApprovalRequest;
  sessionId: string;
  agentName: string;
}

export default function ApprovalsPage() {
  const [items, setItems] = useState<PendingItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let alive = true;
    setError(null);
    (async () => {
      const sessions = await listSessions();
      const withPending = sessions.filter((s) => s.pendingApprovals > 0);
      const collected: PendingItem[] = [];
      for (const s of withPending) {
        try {
          const detail = await getSession(s.id);
          for (const a of detail.pendingApprovals ?? []) {
            collected.push({ approval: a, sessionId: s.id, agentName: s.agentName });
          }
        } catch {
          /* a session disappearing mid-scan is fine */
        }
      }
      if (alive) setItems(collected);
    })().catch((e: unknown) => {
      if (alive) setError(e instanceof Error ? e.message : String(e));
    });
    return () => {
      alive = false;
    };
  }, [tick]);

  const decide = (item: PendingItem, approved: boolean) => {
    setBusy(item.approval.id);
    streamApprove(
      { agent: item.agentName, approvalId: item.approval.id, approved },
      () => undefined,
    )
      .catch(() => undefined)
      .finally(() => {
        setBusy(null);
        setTick((t) => t + 1);
      });
  };

  return (
    <Shell>
      <PageHead
        title="Approvals"
        desc="Human-in-the-loop queue. Runs that need a person before touching the outside world land here and stay blocked until you decide."
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

      {items == null ? (
        <SkeletonRows rows={3} />
      ) : items.length === 0 ? (
        <Empty
          title="Queue is clear"
          hint="No pending approvals. Runs that request approval will appear here automatically."
        />
      ) : (
        <div style={{ display: "grid", gap: 12 }}>
          {items.map((item) => {
            const a = item.approval;
            return (
              <Card
                key={a.id}
                title={
                  <span className="row wrap">
                    <span className="mono small" style={{ color: "var(--warn)" }}>{a.action}</span>
                    <Badge tone="accent">{item.agentName}</Badge>
                    <Link href={`/sessions/${item.sessionId}`} className="link-cell mono small">
                      session {shortID(item.sessionId, 10)}
                    </Link>
                    <span className="faint small">{fmtTime(a.createdAt)}</span>
                  </span>
                }
                actions={
                  <span className="row">
                    <Button
                      size="sm"
                      variant="danger"
                      disabled={busy === a.id}
                      onClick={() => decide(item, false)}
                    >
                      deny
                    </Button>
                    <Button
                      size="sm"
                      variant="primary"
                      disabled={busy === a.id}
                      onClick={() => decide(item, true)}
                    >
                      {busy === a.id ? "resuming…" : "approve"}
                    </Button>
                  </span>
                }
              >
                <div className="small" style={{ marginBottom: 8 }}>{a.summary}</div>
                {a.context && Object.keys(a.context).length > 0 && <CodeBlock value={a.context} />}
              </Card>
            );
          })}
        </div>
      )}
    </Shell>
  );
}
