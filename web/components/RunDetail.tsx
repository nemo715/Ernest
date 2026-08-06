"use client";

// Run detail — the W5 showcase. Header stats, then three tabs:
//   Trace    – span waterfall (llm / tool / approval / step), expandable rows
//   Context  – exactly what the model saw: assembled system prompt, retrieved
//              knowledge chunks, history window. Lock it in with
//              expect.contextContains in your eval suite.
//   JSON     – the raw run trace

import { useEffect, useMemo, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import {
  Badge,
  Card,
  CodeBlock,
  CopyButton,
  Notice,
  PageHead,
  SkeletonRows,
  StatusBadge,
  Tabs,
} from "@/components/ui";
import { getRunTrace } from "@/lib/api";
import type { RunTrace, TraceSpan } from "@/lib/types";
import { fmtClock, fmtCost, fmtDuration, fmtNum } from "@/lib/format";

const KIND_TONE: Record<string, string> = {
  llm: "accent",
  tool: "warn",
  approval: "ok",
  step: "muted",
};

export default function RunDetailPage() {
  const params = useParams<{ id: string }>();
  const runId = params.id;

  const [trace, setTrace] = useState<RunTrace | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState("trace");
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    getRunTrace(runId)
      .then(setTrace)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [runId]);

  const wf = useMemo(() => (trace ? buildWaterfall(trace.spans) : null), [trace]);

  if (error) {
    return (
      <Shell>
        <PageHead title="Run not found" />
        <div className="notice danger">{error}</div>
        <p className="muted small" style={{ marginTop: 12 }}>
          <Link href="/runs" style={{ color: "var(--accent)" }}>← back to runs</Link>
        </p>
      </Shell>
    );
  }

  if (!trace || !wf) {
    return (
      <Shell>
        <PageHead title="Run trace" />
        <SkeletonRows rows={6} />
      </Shell>
    );
  }

  const m = trace.metrics;
  const usage = m?.tokens;

  return (
    <Shell>
      <PageHead
        title={<span className="mono">{runId}</span>}
        desc={
          <span className="row wrap" style={{ marginTop: 6 }}>
           <StatusBadge status={m?.status ?? (trace.spans.length > 0 ? "completed" : "running")} />
            {trace.agent && <Badge tone="accent">{trace.agent}</Badge>}
            <Badge tone="muted">{trace.source || "internal"}</Badge>
            {trace.startedAt && <span className="faint">{fmtClock(trace.startedAt)}</span>}
          </span>
        }
        actions={<CopyButton text={runId} label="copy id" />}
      />

      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <div className="stat">
          <div className="stat-label">Duration</div>
          <div className="stat-value">{fmtDuration(m?.durationMs)}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Spans</div>
          <div className="stat-value">{trace.spans.length}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Iterations</div>
          <div className="stat-value">{m ? fmtNum(m.iterations) : "—"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Tokens</div>
          <div className="stat-value">
            {usage ? `${fmtNum(usage.inputTokens)} → ${fmtNum(usage.outputTokens)}` : "—"}
          </div>
        </div>
        <div className="stat">
          <div className="stat-label">Cost</div>
          <div className="stat-value">{fmtCost(m?.costCents)}</div>
        </div>
      </div>

      {m?.status === "failed" && m && (
        <div className="notice danger" style={{ marginBottom: 16 }}>
          Run failed. Inspect the failing span below (red bars), then open the{" "}
          <Link href="/failures" style={{ color: "inherit", textDecoration: "underline" }}>
            failures feed
          </Link>{" "}
          for the recorded reproduction data.
        </div>
      )}

      <Card>
        <Tabs
          tabs={[
            { id: "trace", label: "Trace" },
            { id: "context", label: "Context" },
            { id: "json", label: "JSON" },
          ]}
          active={tab}
          onChange={setTab}
        />
        <div style={{ marginTop: 14 }}>
          {tab === "trace" && (
            <div className="waterfall">
              <div className="wf-row" style={{ borderBottom: "1px solid var(--border-strong)" }}>
                <span className="faint small">span</span>
                <span className="faint small">timeline</span>
                <span className="faint small" style={{ textAlign: "right" }}>duration</span>
              </div>
              {wf.sorted.map((s) => {
                const t = +new Date(s.startedAt);
                const left = isFinite(t) ? ((t - wf.min) / wf.total) * 100 : 0;
                const width = Math.max((s.durationMs / wf.total) * 100, 0.4);
                const isOpen = open === s.id;
                return (
                  <div key={s.id}>
                    <button
                      className="wf-row wf-btn"
                      onClick={() => setOpen(isOpen ? null : s.id)}
                      aria-expanded={isOpen}
                    >
                      <span
                        className="wf-name"
                        style={{ paddingLeft: Math.min(wf.depthOf(s), 4) * 14 }}
                      >
                        <span className="wf-kind" style={{ color: "var(--text-secondary)" }}>
                          {s.kind}
                        </span>
                        <span style={{ opacity: wf.depthOf(s) > 0 ? 0.85 : 1 }}>{s.name}</span>
                      </span>
                      <span className="wf-track">
                        <span
                          className={`wf-bar ${s.status === "ok" ? "ok" : s.status === "error" ? "error" : s.status === "blocked" ? "blocked" : ""}`}
                          style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }}
                        />
                      </span>
                      <span className="wf-dur">{fmtDuration(s.durationMs)}</span>
                    </button>
                    {isOpen && (
                      <div className="wf-detail">
                        {(s.input != null || s.output != null) && (
                          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
                            {s.input != null && (
                              <div>
                                <div className="faint small" style={{ marginBottom: 4 }}>input</div>
                                <CodeBlock value={s.input} />
                              </div>
                            )}
                            {s.output != null && (
                              <div>
                                <div className="faint small" style={{ marginBottom: 4 }}>output</div>
                                <CodeBlock value={s.output} />
                              </div>
                            )}
                          </div>
                        )}
                        {s.tokens && (
                          <div className="faint small" style={{ marginTop: 8 }}>
                            {fmtNum(s.tokens.inputTokens)} in → {fmtNum(s.tokens.outputTokens)} out
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
              {trace.spans.length === 0 && (
                <div className="faint small" style={{ padding: "14px 2px" }}>
                  No spans recorded for this run.
                </div>
              )}
            </div>
          )}

          {tab === "context" && <ContextPanel context={trace.context} />}

          {tab === "json" && <CodeBlock value={trace} />}
        </div>
      </Card>
    </Shell>
  );
}

// ---------------------------------------------------------------------------
// Context tab
// ---------------------------------------------------------------------------

function ContextPanel({ context }: { context: RunTrace["context"] }) {
  if (!context) {
    return (
      <Notice tone="info">
        No context captured for this run. Context is persisted for runs executed by this runtime
        (v0.1.7+); ingested traces and older runs don&apos;t carry it.
      </Notice>
    );
  }

  const knowledge = context.knowledge ?? [];
  const sysChars = context.systemPrompt?.length ?? 0;

  return (
    <div>
      <div className="context-stats">
        <span className="context-stat">
          History <b>{context.historySent} / {context.historyTotal}</b> messages sent
        </span>
        <span className="context-stat">
          Knowledge <b>{knowledge.length}</b> chunk{knowledge.length === 1 ? "" : "s"} retrieved
        </span>
        <span className="context-stat">
          System prompt <b>{fmtNum(sysChars)}</b> chars
        </span>
      </div>

      {sysChars === 0 ? (
        <Notice tone="info">
          This agent has no instructions or knowledge configured — the model saw a bare chat
          prompt. Add <span className="mono small">instructions</span> and{" "}
          <span className="mono small">knowledge</span> to the agent in ernest.json.
        </Notice>
      ) : (
        <>
          <div className="faint small" style={{ marginBottom: 6 }}>
            Assembled system prompt — exactly what the model saw
          </div>
          <pre className="code-block" style={{ whiteSpace: "pre-wrap" }}>{context.systemPrompt}</pre>
        </>
      )}

      {knowledge.length > 0 && (
        <div style={{ marginTop: 16 }}>
          <div className="faint small" style={{ marginBottom: 6 }}>
            Retrieved knowledge chunks (in prompt order)
          </div>
          {knowledge.map((chunk, i) => (
            <pre key={i} className="code-block" style={{ whiteSpace: "pre-wrap", marginBottom: 8 }}>
              <span className="faint">{String(i + 1).padStart(2, "0")} ·</span> {chunk}
            </pre>
          ))}
        </div>
      )}

      <div className="faint small" style={{ marginTop: 16 }}>
        Lock this in:{" "}
        <span className="mono">
          expect.contextContains: ["instructions…", "knowledge…"]
        </span>{" "}
        in your eval suite fails the run if instructions/knowledge never reached the model.
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Waterfall geometry
// ---------------------------------------------------------------------------

function buildWaterfall(spans: TraceSpan[]) {
  const byId = new Map(spans.map((s) => [s.id, s]));
  const depths = new Map<string, number>();
  const depthOf = (s: TraceSpan): number => {
    const cached = depths.get(s.id);
    if (cached !== undefined) return cached;
    const d = s.parent && byId.has(s.parent) ? depthOf(byId.get(s.parent)!) + 1 : 0;
    depths.set(s.id, d);
    return d;
  };
  const sorted = [...spans].sort((a, b) => +new Date(a.startedAt) - +new Date(b.startedAt));
  let min = Infinity;
  let max = 0;
  for (const s of sorted) {
    const t = +new Date(s.startedAt);
    if (!isFinite(t)) continue;
    min = Math.min(min, t);
    max = Math.max(max, t + s.durationMs);
  }
  if (!isFinite(min)) min = 0;
  return { sorted, depthOf, min, total: Math.max(max - min, 1) };
}
