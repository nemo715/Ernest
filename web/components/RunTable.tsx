"use client";

// Shared run table (overview + runs page).

import Link from "next/link";
import type { RunSummary } from "@/lib/types";
import { fmtTime, shortID } from "@/lib/format";
import { Badge, StatusBadge } from "@/components/ui";

export function RunTable({ runs }: { runs: RunSummary[] }) {
  return (
    <div className="table-wrap">
      <table className="data">
        <thead>
          <tr>
            <th>Run</th>
            <th>Agent</th>
            <th>Status</th>
            <th className="num">Spans</th>
            <th className="num">Duration</th>
            <th>Source</th>
            <th>Started</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.runId}>
              <td>
                <Link href={`/runs/${r.runId}`} className="link-cell mono">
                  {shortID(r.runId, 12)}
                </Link>
              </td>
              <td>{r.agent || "—"}</td>
              <td>
                <StatusBadge status={r.status} />
              </td>
              <td className="num">{r.spanCount}</td>
              <td className="num">{r.durationMs ? `${r.durationMs}ms` : "—"}</td>
              <td>
                <Badge tone={r.source === "ingested" ? "accent" : "muted"}>
                  {r.source || "internal"}
                </Badge>
              </td>
              <td className="faint">{fmtTime(r.startedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
