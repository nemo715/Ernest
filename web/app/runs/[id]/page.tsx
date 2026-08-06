// Static-export wrapper for the run detail page. Dynamic segments need
// generateStaticParams under output: export; the real page is a client
// component that resolves any run id at runtime. "_" is the build-time
// placeholder — it is never linked, and the Go server's SPA fallback
// serves index.html for deep links, so /runs/<any-id> works after load.

import RunDetail from "@/components/RunDetail";

export function generateStaticParams() {
  return [{ id: "_" }];
}

export default function RunDetailPage() {
  return <RunDetail />;
}
