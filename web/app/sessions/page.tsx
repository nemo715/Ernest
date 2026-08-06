"use client";

// Sessions — persistent conversations, each with its own memory window.
// A session may hold pending approval requests (HITL).

import { useEffect, useState } from "react";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import { Badge, Button, Empty, PageHead, SkeletonRows } from "@/components/ui";
import { deleteSession, listSessions } from "@/lib/api";
import type { SessionInfo } from "@/lib/types";
import { fmtTime, shortID } from "@/lib/format";

export default function SessionsPage() {
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    setError(null);
    listSessions()
      .then(setSessions)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [tick]);

  const remove = (id: string) => {
    if (!window.confirm(`Delete session ${shortID(id, 10)}? Its history is gone.`)) return;
    deleteSession(id)
      .then(() => setTick((t) => t + 1))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };

  return (
    <Shell>
      <PageHead
        title="Sessions"
        desc="Persistent conversations. Each session carries its own message history — the model only ever sees the tail of it, as recorded in run traces."
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

      {sessions == null ? (
        <SkeletonRows rows={5} />
      ) : sessions.length === 0 ? (
        <Empty
          title="No sessions yet"
          hint={
            <>
              Start one from the <a href="/playground">playground</a> — or keep using{" "}
              <span className="mono small">ernest chat</span>, which saves its own sessions.
            </>
          }
        />
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Session</th>
                <th>Agent</th>
                <th className="num">Messages</th>
                <th>Pending</th>
                <th>Updated</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={s.id}>
                  <td>
                    <Link href={`/sessions/${s.id}`} className="link-cell mono">
                      {shortID(s.id, 12)}
                    </Link>
                  </td>
                  <td>
                    <Badge tone="accent">{s.agentName}</Badge>
                  </td>
                  <td className="num">{s.messages}</td>
                  <td>
                    {s.pendingApprovals > 0 ? (
                      <Badge tone="warn" dot>
                        {s.pendingApprovals} pending
                      </Badge>
                    ) : (
                      <span className="faint">—</span>
                    )}
                  </td>
                  <td className="faint">{fmtTime(s.updatedAt)}</td>
                  <td style={{ textAlign: "right" }}>
                    <Button variant="ghost" size="sm" onClick={() => remove(s.id)}>
                      delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Shell>
  );
}
