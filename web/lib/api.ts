// API client for the ernest playground backend (internal/server/server.go).
//
// Backend resolution: the static export is served by the Go binary (same
// origin) in production and by `next dev` during development. The client
// tries the configured base first and falls back to the local playground
// server at http://127.0.0.1:9090. The Go server sends permissive CORS
// headers, so cross-origin calls from the browser work out of the box.

import type {
  AgentInfo,
  ApproveRequest,
  ChatRequest,
  RunEvent,
  Session,
  SessionInfo,
  WSClientMessage,
  WSServerFrame,
} from "@/lib/types";

const FALLBACK_BASE = "http://127.0.0.1:9090";

const configuredBase: string = (typeof process !== "undefined" && process.env.NEXT_PUBLIC_ERNEST_API_URL ? process.env.NEXT_PUBLIC_ERNEST_API_URL : "").replace(/\/$/, "");

/** Bases tried in order; the first one that answers wins and is cached. */
const bases: string[] = [...new Set([configuredBase, FALLBACK_BASE])];

let activeBase = bases[0];

/** True when the response is not from the ernest API (e.g. a dev-server
 * or static-host 404 page). Every real API response is JSON. */
function isNonJSON404(res: Response): boolean {
  return (
    res.status === 404 &&
    !(res.headers.get("content-type") ?? "").includes("application/json")
  );
}

async function fetchWithFallback(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<Response> {
  let lastErr: unknown;
  for (let i = 0; i < bases.length; i++) {
    const base = i === 0 ? activeBase : bases[i];
    try {
      const res = await fetch(`${base}${path}`, { ...init, signal });
      if (isNonJSON404(res) && i < bases.length - 1) {
        // Not the ernest API — try the next base.
        activeBase = bases[i + 1];
        continue;
      }
      activeBase = base;
      return res;
    } catch (err) {
      lastErr = err;
      if (signal?.aborted) throw err;
      // Network failure — try the next base.
    }
  }
  throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
}

async function readError(res: Response): Promise<string> {
  try {
    const body = await res.text();
    try {
      const parsed = JSON.parse(body) as { error?: string };
      if (parsed.error) return parsed.error;
    } catch {
      /* not JSON */
    }
    return body || res.statusText;
  } catch {
    return res.statusText;
  }
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetchWithFallback(path, init);
  if (!res.ok) throw new Error(await readError(res));
  return (await res.json()) as T;
}

export function getAgents(): Promise<AgentInfo[]> {
  return requestJSON("/api/agents");
}

export function listSessions(agent?: string): Promise<SessionInfo[]> {
  const q = agent ? `?agent=${encodeURIComponent(agent)}` : "";
  return requestJSON(`/api/sessions${q}`);
}

export function getSession(id: string): Promise<Session> {
  return requestJSON(`/api/sessions/${encodeURIComponent(id)}`);
}

export async function deleteSession(id: string): Promise<void> {
  await requestJSON(`/api/sessions/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

export async function healthz(): Promise<boolean> {
  try {
    const res = await fetchWithFallback("/healthz");
    return res.ok;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

export type EventHandler = (ev: RunEvent) => void;

/** Parses one SSE frame ("data: {...}") into a RunEvent. */
function parseFrame(frame: string): RunEvent | null {
  let data = "";
  for (const line of frame.split(/\r?\n/)) {
    if (line.startsWith("data:")) {
      data += line.slice(5).trimStart();
    }
  }
  if (!data) return null;
  try {
    return JSON.parse(data) as RunEvent;
  } catch {
    return null;
  }
}

/** Reads a response body, splitting on SSE frame boundaries. */
async function pumpSSE(
  res: Response,
  onEvent: EventHandler,
  signal?: AbortSignal,
): Promise<void> {
  if (!res.body) throw new Error("streaming not supported by server");
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let end = findFrameEnd(buf);
    while (end !== -1) {
      const frame = buf.slice(0, end);
      buf = buf.slice(end + 2); // consume the blank line separator
      const ev = parseFrame(frame);
      if (ev) onEvent(ev);
      end = findFrameEnd(buf);
    }
    if (signal?.aborted) {
      await reader.cancel().catch(() => undefined);
      break;
    }
  }
  const ev = parseFrame(buf.trimEnd());
  if (ev) onEvent(ev);
}

function findFrameEnd(s: string): number {
  const a = s.indexOf("\n\n");
  const b = s.indexOf("\r\n\r\n");
  if (a === -1) return b;
  if (b === -1) return a;
  return Math.min(a, b);
}

/** POST /api/chat and stream RunEvents until the stream closes. */
export async function streamChat(
  body: ChatRequest,
  onEvent: EventHandler,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetchWithFallback(
    "/api/chat",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
    signal,
  );
  if (!res.ok) throw new Error(await readError(res));
  await pumpSSE(res, onEvent, signal);
}

/** POST /api/approve (HITL) and stream the resumed run. */
export async function streamApprove(
  body: ApproveRequest,
  onEvent: EventHandler,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetchWithFallback(
    "/api/approve",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
    signal,
  );
  if (!res.ok) throw new Error(await readError(res));
  await pumpSSE(res, onEvent, signal);
}

// ---------------------------------------------------------------------------
// WebSocket transport (GET /ws/chat)
// ---------------------------------------------------------------------------

/** GET /api/runs/{id}/trace — instrumented spans of a finished run. */
export async function getRunTrace(runId: string): Promise<unknown[]> {
  return requestJSON(`/api/runs/${encodeURIComponent(runId)}/trace`);
}

export type WSStatus = "connecting" | "open" | "closed";

export interface ErnestWSOptions {
  onEvent: EventHandler;
  onFrame?: (frame: WSServerFrame) => void;
  onStatus?: (status: WSStatus) => void;
}

/**
 * One persistent /ws/chat connection. The server runs at most one run
 * per connection; steering queues a follow-up run on the same session.
 * Falls back to SSE transport (streamChat/streamApprove) by reporting
 * a failed connect() — the caller decides.
 */
export class ErnestWS {
  private ws: WebSocket | null = null;
  private closed = false;
  private readonly onEvent: EventHandler;
  private readonly onFrame?: (frame: WSServerFrame) => void;
  private readonly onStatus?: (status: WSStatus) => void;

  constructor(opts: ErnestWSOptions) {
    this.onEvent = opts.onEvent;
    this.onFrame = opts.onFrame;
    this.onStatus = opts.onStatus;
  }

  /** Tries every known base; resolves true on the first open socket. */
  async connect(): Promise<boolean> {
    for (const base of bases) {
      if (this.closed) return false;
      const url = base.replace(/^http/, "ws") + "/ws/chat";
      const ws = new WebSocket(url);
      this.ws = ws;
      this.onStatus?.("connecting");
      try {
        await new Promise<void>((resolve, reject) => {
          const timer = setTimeout(() => {
            ws.close();
            reject(new Error("ws connect timeout"));
          }, 5000);
          ws.onopen = () => {
            clearTimeout(timer);
            resolve();
          };
          ws.onerror = () => {
            clearTimeout(timer);
            reject(new Error("ws connect failed"));
          };
        });
      } catch {
        continue; // try the next base
      }
      ws.onmessage = (e) => this.handleMessage(e.data);
      ws.onclose = () => {
        if (!this.closed) this.onStatus?.("closed");
      };
      this.onStatus?.("open");
      return true;
    }
    this.ws = null;
    return false;
  }

  get open(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  private handleMessage(data: unknown): void {
    if (typeof data !== "string") return;
    let frame: WSServerFrame | RunEvent;
    try {
      frame = JSON.parse(data) as WSServerFrame | RunEvent;
    } catch {
      return;
    }
    switch (frame.type) {
      case "ready":
      case "pong":
      case "ack":
        this.onFrame?.(frame);
        return;
      case "error":
        this.onFrame?.(frame);
        // Surface protocol errors as run errors so the UI can show them.
        this.onEvent({ type: "run.error", runId: "", error: frame.error });
        return;
      default:
        this.onEvent(frame as RunEvent);
    }
  }

  send(msg: WSClientMessage): void {
    if (this.open) {
      this.ws?.send(JSON.stringify(msg));
    }
  }

  chat(body: ChatRequest): void {
    this.send({ type: "chat", agent: body.agent, input: body.input, sessionId: body.sessionId, userId: body.userId });
  }

  steer(input: string): void {
    this.send({ type: "steer", input });
  }

  interrupt(): void {
    this.send({ type: "interrupt" });
  }

  approve(body: ApproveRequest): void {
    this.send({
      type: "approve",
      agent: body.agent,
      approvalId: body.approvalId,
      approved: body.approved,
      note: body.note,
    });
  }

  ping(): void {
    this.send({ type: "ping" });
  }

  close(): void {
    this.closed = true;
    this.ws?.close();
    this.ws = null;
  }
}
