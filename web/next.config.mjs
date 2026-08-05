// Next.js config for the ernest playground UI.
//
// - `output: "export"` builds a fully static site into `out/`, which the
//   Go backend serves directly: `ernest playground --static web/out`.
// - `output: "export"` ignores rewrites, so the API client resolves the
//   backend at runtime: same-origin first (served by the Go binary), then
//   http://127.0.0.1:9090 (bare `npm run dev`). Override the fallback by
//   building with NEXT_PUBLIC_ERNEST_API_URL set.
import path from "node:path";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  outputFileTracingRoot: path.join(import.meta.dirname),
};

export default nextConfig;
