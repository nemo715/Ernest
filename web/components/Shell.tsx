"use client";

// Console shell: sticky sidebar with the bulbul wordmark, sectioned
// navigation and a live server status, plus the content column.
// Every console page renders inside <Shell>.

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState, ReactNode } from "react";
import { healthz } from "@/lib/api";
import { BulbulLogo } from "@/components/BulbulLogo";

const NAV = [
  { section: "Operate", links: [
    { href: "/", label: "Overview" },
    { href: "/playground", label: "Playground" },
    { href: "/approvals", label: "Approvals" },
  ]},
  { section: "Observe", links: [
    { href: "/runs", label: "Runs & traces" },
    { href: "/sessions", label: "Sessions" },
    { href: "/failures", label: "Failures" },
    { href: "/audit", label: "Audit log" },
  ]},
  { section: "Configure", links: [
    { href: "/agents", label: "Agents" },
  ]},
];

export function Shell({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const [online, setOnline] = useState<boolean | null>(null);

  useEffect(() => {
    let alive = true;
    const tick = async () => {
      const ok = await healthz();
      if (alive) setOnline(ok);
    };
    tick();
    const t = setInterval(tick, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  const isActive = (href: string) =>
    href === "/" ? pathname === "/" : pathname.startsWith(href);

  return (
    <div className="shell">
      <aside className="sidebar">
        <Link href="/" className="brand" style={{ textDecoration: "none" }}>
          <BulbulLogo size={26} />
          <span className="brand-name">
            ernest<em>·console</em>
          </span>
        </Link>
        <nav className="nav">
          {NAV.map((group) => (
            <div key={group.section}>
              <div className="nav-section">{group.section}</div>
              {group.links.map((l) => (
                <Link
                  key={l.href}
                  href={l.href}
                  className={`nav-link ${isActive(l.href) ? "active" : ""}`}
                >
                  {l.label}
                  {l.href === "/approvals" && <PendingDot />}
                </Link>
              ))}
            </div>
          ))}
        </nav>
        <div className="sidebar-footer">
          <div className="server-status">
            <span
              className={`pulse ${online === null ? "" : online ? "online" : "offline"}`}
            />
            {online === null ? "connecting…" : online ? "server online" : "server offline"}
          </div>
          <div className="mono" style={{ fontSize: 10.5 }}>
            http://127.0.0.1:9090 · ernest 0.1.x
          </div>
        </div>
      </aside>
      <main className="content">{children}</main>
    </div>
  );
}

function PendingDot() {
  return <span className="dot" />;
}
