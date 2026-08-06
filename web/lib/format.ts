// Formatting helpers shared across console pages.

export function fmtTime(iso: string | undefined | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "—";
  const now = Date.now();
  const diff = now - d.getTime();
  if (diff < 45_000) return "just now";
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)}h ago`;
  if (diff < 7 * 86_400_000) return `${Math.round(diff / 86_400_000)}d ago`;
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function fmtClock(iso: string | undefined | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "—";
  return d.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function fmtDuration(ms: number | undefined | null): string {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function fmtNum(n: number | undefined | null): string {
  if (n == null) return "—";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 10_000) return `${(n / 1000).toFixed(1)}k`;
  return n.toLocaleString("en-US");
}

export function fmtCost(cents: number | undefined | null): string {
  if (cents == null || cents === 0) return "—";
  if (cents < 1) return "<$0.01";
  return `$${(cents / 100).toFixed(2)}`;
}

export function shortID(id: string, head = 8): string {
  if (id.length <= head + 4) return id;
  return `${id.slice(0, head)}…${id.slice(-3)}`;
}

/** Pretty JSON with keyword coloring, rendered as HTML. */
export function highlightJSON(value: unknown): string {
  let raw: string;
  if (typeof value === "string") {
    try {
      raw = JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      raw = JSON.stringify(value);
    }
  } else {
    raw = JSON.stringify(value, null, 2);
  }
  if (raw.length > 4000) {
    raw = raw.slice(0, 4000) + "\n… (truncated)";
  }
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/("(?:\\.|[^"\\])*")(\s*:)?/g, (m, str, colon) => {
      // Keys get the accent color, string values the ok color.
      if (colon) return `<span class="k">${str}</span>${colon ?? ""}`;
      return `<span class="s">${str}</span>`;
    })
    .replace(/\b(true|false|null)\b/g, '<span class="k">$1</span>')
    .replace(/\b(-?\d+(?:\.\d+)?)\b/g, '<span class="s">$1</span>');
}

export function statusTone(status: string): "ok" | "warn" | "danger" | "muted" {
  switch (status) {
    case "completed":
    case "ok":
    case "approved":
    case "online":
      return "ok";
    case "failed":
    case "error":
    case "rejected":
    case "interrupted":
    case "offline":
      return "danger";
    case "awaiting_approval":
    case "pending":
    case "running":
    case "blocked":
      return "warn";
    default:
      return "muted";
  }
}
