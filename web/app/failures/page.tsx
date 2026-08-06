"use client";

// Failures — the recorded failure feed (ernest.json: "failures").
// Each record is the exact reproduction data for one failed run:
// input, output, tool calls and results, timestamp. The raw feed is
// also available over HTTP: GET /api/failures.

import { useEffect, useState } from "react";
import Link from "next/link";
import { Shell } from "@/components/Shell";
import { Badge, Button, CodeBlock, Empty, Notice, PageHead, SkeletonRows } from "@/components/ui";
import { getFailures } from "@/lib/api";
import type { FailureRecord } from "@/lib/types";
import { fmtTime, shortID } from "@/lib/format";

export default function FailuresPage() {
  const [records, setRecords] = useState<FailureRecord[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const [open, setOpen] = useState<number | null>(null);

  useEffect(() => {
    setError(null);
    getFailures(100)
      .then((r) => setRecords(r.records))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [tick]);

  const noFeed = error?.includes("no failures feed configured");

  return (
    <Shell>
      <PageHead
        title="Failures"
        desc="Every failed run, recorded with full reproduction data. Fix the tool, tighten the instructions, or turn these into eval guardrails — the feed keeps no secrets."
        actions={
          <Button variant="ghost" size="sm" onClick={() => setTick((t) => t + 1)}>
            refresh
          </Button>
        }
      />

      {noFeed ? (
        <Notice tone="info">
          <strong>No failures feed configured.</strong> Add{" "}
          <span className="mono small">"failures": "failures.jsonl"</span> to ernest.json and
          restart the server — failed runs are then recorded here and served at{" "}
          <span className="mono small">GET /api/failures</span>.
        </Notice>
      ) : (
        error && (
          <div className="notice danger" style={{ marginBottom: 12 }}>
            {error}
          </div>
        )
      )}

      {records == null ? (
        <SkeletonRows rows={3} />
      ) : records.length === 0 ? (
        <Empty
          title="Nothing failed yet"
          hint="Records appear the moment a run fails. Keep going — every entry here is a guardrail waiting to be written."
        />
      ) : (
        <div>
          <div className="faint small" style={{ marginBottom: 8 }}>
            {records.length} record{records.length === 1 ? "" : "s"}, newest first
          </div>
          {records.map((f, i) => (
            <div key={i} className="failure-card">
              <div className="f-head">
                <span className="f-input">{f.input.length > 140 ? `${f.input.slice(0, 140)}…` : f.input}</span>
                <span className="f-meta">
                  {f.at ? fmtTime(f.at) : ""}
                  {f.agent ? ` · ${f.agent}` : ""}
                </span>
              </div>
              <div className="row wrap" style={{ marginBottom: 6 }}>
                {f.status && <Badge tone="danger" dot>{f.status}</Badge>}
                {f.runId && (
                  <Link href={`/runs/${f.runId}`} className="link-cell mono small">
                    {shortID(f.runId, 10)}
                  </Link>
                )}
                <span className="faint small">
                  {f.toolCalls.length} tool call{f.toolCalls.length === 1 ? "" : "s"}
                  {f.error ? " · run failed" : ""}
                </span>
              </div>
              {f.error && (
                <div className="mono small" style={{ color: "var(--danger)", marginBottom: 6 }}>
                  {f.error.length > 260 ? `${f.error.slice(0, 260)}…` : f.error}
                </div>
              )}
              {f.toolCalls.length > 0 && (
                <div className="row wrap" style={{ marginBottom: 6 }}>
                  {f.toolCalls.map((tc, j) => (
                    <Badge key={j} tone="warn">↳ {tc.name}</Badge>
                  ))}
                </div>
              )}
              {f.toolResults.length > 0 && (
                <button
                  className="btn btn-ghost btn-sm"
                  onClick={() => setOpen(open === i ? null : i)}
                  aria-expanded={open === i}
                >
                  {open === i ? "hide tool results" : "show tool results"}
                </button>
              )}
              {open === i && (
                <div style={{ marginTop: 8 }}>
                  <CodeBlock value={f.toolResults} />
                </div>
              )}
            </div>
          ))}
          <div className="faint small" style={{ marginTop: 14 }}>
            Turn these into guardrails: add{" "}
            <span className="mono">expect.toolResults: [{"{ name, errorContains }"}]</span> or{" "}
            <span className="mono">expect.contextContains</span> to your eval suite, then run{" "}
            <span className="mono">ernest eval</span>.
          </div>
        </div>
      )}
    </Shell>
  );
}
