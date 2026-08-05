"use client";

import { useState } from "react";
import type { ApprovalRequest } from "@/lib/types";
import type { ApprovalStatus, ChatItem } from "@/components/Playground";
import { pretty } from "@/components/Playground";

interface Props {
  item: ChatItem;
  streaming: boolean;
  busyApproval: string | null;
  onDecide: (ap: ApprovalRequest, approved: boolean, note: string) => void;
}

export function MessageView({ item, streaming, busyApproval, onDecide }: Props) {
  switch (item.kind) {
    case "user":
      return (
        <div className="row user">
          <div className="bubble user-bubble">{item.text}</div>
        </div>
      );

    case "assistant":
      return (
        <div className="row assistant">
          <div className="avatar">✦</div>
          <div className="assistant-body">
            <div className="bubble assistant-bubble">
              {item.text ? (
                <p className="msg-text">{item.text}</p>
              ) : item.streaming ? (
                <p className="msg-text caret" />
              ) : (
                <p className="msg-text muted">…</p>
              )}
              {item.streaming && item.text && <span className="caret" />}
            </div>
            {item.tools.map((t) => (
              <details key={t.id} className="tool-card" open={t.running}>
                <summary>
                  <span className={`tool-status ${t.running ? "running" : t.resultError ? "err" : "ok"}`} />
                  <span className="tool-name">{t.name}</span>
                  <span className="tool-id">{t.id.slice(0, 8)}</span>
                  {t.running && <span className="tool-running">running…</span>}
                </summary>
                <div className="tool-args">
                  <div className="tool-label">arguments</div>
                  <pre>{pretty(t.arguments)}</pre>
                </div>
                {t.result !== undefined && (
                  <div className="tool-result">
                    <div className="tool-label">result</div>
                    <pre>{pretty(t.result)}</pre>
                  </div>
                )}
                {t.resultError && (
                  <div className="tool-result err">
                    <div className="tool-label">error</div>
                    <pre>{t.resultError}</pre>
                  </div>
                )}
              </details>
            ))}
            {item.usage && (
              <div className="usage">
                {item.usage.inputTokens} in · {item.usage.outputTokens} out
              </div>
            )}
          </div>
        </div>
      );

    case "approval":
      return (
        <ApprovalCard
          req={item.approval.req}
          status={item.approval.status}
          resolving={item.approval.status === "resolving"}
          busy={busyApproval === item.approval.req.id}
          onDecide={onDecide}
        />
      );

    case "step":
      return (
        <div className="row step">
          <span className="step-chip">step {item.name}</span>
        </div>
      );

    case "error":
      return (
        <div className="row error">
          <div className="bubble error-bubble">⚠ {item.text}</div>
        </div>
      );
  }
}

// ---------------------------------------------------------------------------
// HITL approval card
// ---------------------------------------------------------------------------

function ApprovalCard({
  req,
  status,
  resolving,
  busy,
  onDecide,
}: {
  req: ApprovalRequest;
  status: ApprovalStatus;
  resolving: boolean;
  busy: boolean;
  onDecide: (ap: ApprovalRequest, approved: boolean, note: string) => void;
}) {
  const [note, setNote] = useState("");
  const disabled = resolving || busy || status !== "pending";

  return (
    <div className={`row approval ${status}`}>
      <div className="approval-card">
        <div className="approval-head">
          <span className="approval-icon">🛡</span>
          <span className="approval-title">Approval required</span>
          <span className="approval-status">{status}</span>
        </div>
        <div className="approval-action">{req.action}</div>
        <p className="approval-summary">{req.summary}</p>
        {req.context && Object.keys(req.context).length > 0 && (
          <details className="approval-context">
            <summary>context</summary>
            <pre>{pretty(req.context)}</pre>
          </details>
        )}
        {status === "pending" && (
          <>
            <input
              className="approval-note"
              placeholder="Note (optional)"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              disabled={disabled}
            />
            <div className="approval-actions">
              <button
                className="btn approve"
                disabled={disabled}
                onClick={() => onDecide(req, true, note)}
              >
                {resolving ? "…" : "✓ Approve"}
              </button>
              <button
                className="btn reject"
                disabled={disabled}
                onClick={() => onDecide(req, false, note)}
              >
                {resolving ? "…" : "✕ Reject"}
              </button>
            </div>
          </>
        )}
        {(status === "approved" || status === "rejected") && req.note && (
          <p className="approval-note-text">note: {req.note}</p>
        )}
      </div>
    </div>
  );
}
