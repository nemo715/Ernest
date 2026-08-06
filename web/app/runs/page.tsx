"use client";

// Runs & traces — every run the runtime has executed, newest first.
// Open a run to see its trace waterfall and the exact context the model saw.

import { useEffect, useMemo, useState } from "react";
import { Shell } from "@/components/Shell";
import { Button, Empty, PageHead, SkeletonRows } from "@/components/ui";
import { RunTable } from "@/components/RunTable";
import { getRuns } from "@/lib/api";
import type { RunSummary } from "@/lib/types";

const FILTERS = ["all", "completed", "failed", "running", "awaiting_approval", "interrupted"] as const;
type Filter = (typeof FILTERS)[number];

export default function RunsPage() {
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [tick, setTick] = useState(0);

  useEffect(() => {
    setError(null);
    getRuns()
      .then((r) => setRuns(r.runs))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [tick]);

  const filtered = useMemo(
    () => (runs ? (filter === "all" ? runs : runs.filter((r) => r.status === filter)) : []),
    [runs, filter],
  );

  return (
    <Shell>
      <PageHead
        title="Runs & traces"
        desc="Every run the runtime executed, across sessions and ingest. A run is one agent pass over one input — open it to inspect the trace waterfall and the exact context the model saw."
        actions={
          <Button variant="ghost" size="sm" onClick={() => setTick((t) => t + 1)}>
            refresh
          </Button>
        }
      />

      <div className="row wrap" style={{ marginBottom: 14 }}>
        {FILTERS.map((f) => (
          <button
            key={f}
            className={`chip ${filter === f ? "active" : ""}`}
            onClick={() => setFilter(f)}
          >
            {f === "all" ? "all" : f.replace("_", " ")}
            {runs && f !== "all" && (
              <span className="faint">{runs.filter((r) => r.status === f).length}</span>
            )}
          </button>
        ))}
      </div>

      {error && (
        <div className="notice danger" style={{ marginBottom: 12 }}>
          {error}
        </div>
      )}

      {runs == null ? (
        <SkeletonRows rows={6} />
      ) : runs.length === 0 ? (
        <Empty
          title="No runs yet"
          hint={
            <>
              Send a message from the <a href="/playground">playground</a> or with{" "}
              <span className="mono small">ernest chat</span> — every run shows up here with its
              trace.
            </>
          }
        />
      ) : filtered.length === 0 ? (
        <Empty title={`No runs with status “${filter.replace("_", " ")}”`} hint="Try another filter." />
      ) : (
        <>
          <div className="faint small" style={{ marginBottom: 8 }}>
            {filtered.length} run{filtered.length === 1 ? "" : "s"}
          </div>
          <RunTable runs={filtered} />
        </>
      )}
    </Shell>
  );
}
