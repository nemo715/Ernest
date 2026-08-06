"use client";

// Session detail — the full message timeline plus any pending HITL
// approval requests. Approving/denying resumes the blocked run.

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import {
  Badge,
  Button,
  Card,
  CodeBlock,
  CopyButton,
  Empty,
  PageHead,
  SkeletonRows,
} from "@/components/ui";
import { deleteSession, getSession, streamApprove } from "@/lib/api";
import type { ApprovalRequest, Message, Session } from "@/lib/types";
import { fmtClock, shortID } from "@/lib/format";

export default function SessionDetailPage() {
  const params = useParams<{ id: string }>();
  // Static export: deep links (/sessions/<id>) are served via the "_"
  // catchall page — read the real id from the URL path when needed.
  const [sessionId, setSessionId] = useState<string>(params.id);

  useEffect(() => {
    const seg = window.location.pathname.split("/").filter(Boolean).pop();
    setSessionId(seg && seg !== "_" ? seg : params.id);
  }, [params.id]);

  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const reload = () => {
    if (!sessionId || sessionId === "_") return; // placeholder id: wait for the real one
    setError(null);
    getSession(sessionId)
      .then(setSession)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };

  useEffect(reload, [sessionId]);

  const decide = (approval: ApprovalRequest, approved: boolean) => {
    setBusy(approval.id);
    streamApprove(
      { agent: session?.agentName ?? approval.agentName, approvalId: approval.id, approved },
      () => undefined,
    )
      .catch(() => undefined)
      .finally(() => {
        setBusy(null);
        reload();
      });
  };

  const remove = () => {
    if (!window.confirm("Delete this session? Its history is gone.")) return;
    deleteSession(sessionId)
      .then(() => {
        window.location.href = "/sessions";
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };

  if (error) {
    return (
      <Shell>
        <PageHead title="Session not found" />
        <div className="notice danger">{error}</div>
        <p className="muted small" style={{ marginTop: 12 }}>
          <Link href="/sessions" style={{ color: "var(--accent)" }}>← back to sessions</Link>
        </p>
      </Shell>
    );
  }

  if (!session) {
    return (
      <Shell>
        <PageHead title="Session" />
        <SkeletonRows rows={6} />
      </Shell>
    );
  }

  const pending = session.pendingApprovals ?? [];

  return (
    <Shell>
      <PageHead
        title={<span className="mono">{sessionId}</span>}
        desc={
          <span className="row wrap" style={{ marginTop: 6 }}>
            <Badge tone="accent">{session.agentName}</Badge>
            <span className="faint">
              {session.messages.length} messages · created {fmtClock(session.createdAt)}
            </span>
          </span>
        }
        actions={
          <div className="row">
            <CopyButton text={sessionId} label="copy id" />
            <Button variant="danger" size="sm" onClick={remove}>
              delete
            </Button>
          </div>
        }
      />

      {pending.length > 0 && (
        <Card
          title={
            <span className="row">
              Pending approvals
              <Badge tone="warn" dot>{pending.length}</Badge>
            </span>
          }
          desc="The run is blocked until you decide. Approving resumes it; denying fails the run."
        >
          <div style={{ display: "grid", gap: 12 }}>
            {pending.map((a) => (
              <div key={a.id} className="approval">
                <div className="spread wrap">
                  <span className="row wrap">
                    <span className="mono small" style={{ color: "var(--warn)" }}>{a.action}</span>
                    <span className="small">{a.summary}</span>
                  </span>
                  <span className="row">
                    <Button size="sm" variant="danger" disabled={busy === a.id} onClick={() => decide(a, false)}>
                      deny
                    </Button>
                    <Button size="sm" variant="primary" disabled={busy === a.id} onClick={() => decide(a, true)}>
                      {busy === a.id ? "resuming…" : "approve"}
                    </Button>
                  </span>
                </div>
                {a.context && Object.keys(a.context).length > 0 && (
                  <div style={{ marginTop: 8 }}>
                    <CodeBlock value={a.context} />
                  </div>
                )}
              </div>
            ))}
          </div>
        </Card>
      )}

      <Card title="Timeline" desc={`${session.messages.length} messages in order`}>
        {session.messages.length === 0 ? (
          <Empty title="No messages" hint="This session was created but never written to." />
        ) : (
          <div>
            {session.messages.map((msg, i) => (
              <MessageRow key={i} msg={msg} />
            ))}
          </div>
        )}
      </Card>
    </Shell>
  );
}

function MessageRow({ msg }: { msg: Message }) {
  return (
    <div className="msg">
      <div className={`msg-role ${msg.role === "user" ? "user" : ""}`}>{msg.role}</div>
      <div className="msg-body">
        {msg.content && <div>{msg.content}</div>}

        {msg.parts?.filter((p) => p.type === "tool_call").map((p, i) => (
          <div key={i} className="msg-tool">
            <span className="msg-tool-name">↳ {p.toolCall?.name}</span>
            <div style={{ marginTop: 6 }}>
              <CodeBlock value={p.toolCall?.arguments} />
            </div>
          </div>
        ))}

        {msg.toolCalls?.map((tc, i) => (
          <div key={i} className="msg-tool">
            <span className="msg-tool-name">↳ {tc.name}</span>
            <div style={{ marginTop: 6 }}>
              <CodeBlock value={tc.arguments} />
            </div>
          </div>
        ))}

        {msg.role === "tool" && (
          <div className="msg-tool">
            <span className="msg-tool-name">result{msg.name ? ` · ${msg.name}` : ""}</span>
            <div style={{ marginTop: 6 }} className="small">
              {typeof msg.content === "string" ? msg.content : JSON.stringify(msg.content)}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
