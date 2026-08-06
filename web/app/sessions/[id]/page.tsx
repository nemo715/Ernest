// Static-export wrapper for the session detail page — see
// app/runs/[id]/page.tsx for why this indirection exists.

import SessionDetail from "@/components/SessionDetail";

export function generateStaticParams() {
  return [{ id: "_" }];
}

export default function SessionDetailPage() {
  return <SessionDetail />;
}
