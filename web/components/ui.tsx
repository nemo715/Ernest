"use client";

// Small, composable UI primitives for the console. No component
// library: these are deliberately thin wrappers over the design-system
// classes in globals.css, following the same patterns a product team
// would use (variants, composition, aria attributes, focus rings).

import { ReactNode, useState } from "react";
import { highlightJSON } from "@/lib/format";

// ---------------------------------------------------------------------------
// Badge
// ---------------------------------------------------------------------------

export function Badge({
  tone = "muted",
  dot = false,
  children,
}: {
  tone?: "ok" | "warn" | "danger" | "accent" | "muted";
  dot?: boolean;
  children: ReactNode;
}) {
  return (
    <span className={`badge ${tone}`}>
      {dot && <span className="bdot" />}
      {children}
    </span>
  );
}

export function StatusBadge({ status }: { status: string }) {
  const tone =
    status === "completed" || status === "ok"
      ? "ok"
      : status === "failed" ||
          status === "interrupted" ||
          status === "error" ||
          status === "rejected"
        ? "danger"
        : status === "awaiting_approval" ||
            status === "pending" ||
            status === "running" ||
            status === "blocked"
          ? "warn"
          : "muted";
  return <Badge tone={tone} dot>{status.replace("_", " ")}</Badge>;
}

// ---------------------------------------------------------------------------
// Card
// ---------------------------------------------------------------------------

export function Card({
  title,
  desc,
  actions,
  children,
}: {
  title?: ReactNode;
  desc?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="card">
      {(title || actions) && (
        <div className="card-head">
          <div className="grow">
            {title && <h3 className="card-title">{title}</h3>}
            {desc && <p className="card-desc" style={{ marginBottom: 0 }}>{desc}</p>}
          </div>
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Button
// ---------------------------------------------------------------------------

export function Button({
  variant = "default",
  size,
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "danger" | "ghost";
  size?: "sm";
}) {
  const cls = [
    "btn",
    variant === "primary" ? "btn-primary" : "",
    variant === "danger" ? "btn-danger" : "",
    variant === "ghost" ? "btn-ghost" : "",
    size === "sm" ? "btn-sm" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button className={cls} {...rest}>
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Page head
// ---------------------------------------------------------------------------

export function PageHead({
  title,
  desc,
  actions,
}: {
  title: ReactNode;
  desc?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="page-head">
      <div>
        <h1 className="page-title">{title}</h1>
        {desc && <p className="page-desc">{desc}</p>}
      </div>
      {actions && <div className="row wrap">{actions}</div>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Code block (syntax-tinted JSON + plain text)
// ---------------------------------------------------------------------------

export function CodeBlock({ value }: { value: unknown }) {
  return (
    <pre
      className="code-block"
      dangerouslySetInnerHTML={{
        __html: highlightJSON(value),
      }}
    />
  );
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size="sm"
      variant="ghost"
      onClick={() => {
        navigator.clipboard?.writeText(text).catch(() => undefined);
        setCopied(true);
        setTimeout(() => setCopied(false), 1200);
      }}
    >
      {copied ? "copied" : label ?? "copy"}
    </Button>
  );
}

// ---------------------------------------------------------------------------
// Empty state + skeletons
// ---------------------------------------------------------------------------

export function Empty({
  title,
  hint,
}: {
  title: string;
  hint?: ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-title">{title}</div>
      {hint && <div className="empty-hint">{hint}</div>}
    </div>
  );
}

export function SkeletonRows({ rows = 4 }: { rows?: number }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="skeleton" style={{ height: 26 }} />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tabs
// ---------------------------------------------------------------------------

export function Tabs({
  tabs,
  active,
  onChange,
}: {
  tabs: { id: string; label: string }[];
  active: string;
  onChange: (id: string) => void;
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={t.id === active}
          className={`tab ${t.id === active ? "active" : ""}`}
          onClick={() => onChange(t.id)}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Notice
// ---------------------------------------------------------------------------

export function Notice({
  tone = "default",
  children,
}: {
  tone?: "default" | "info" | "danger";
  children: ReactNode;
}) {
  return <div className={`notice ${tone}`}>{children}</div>;
}
