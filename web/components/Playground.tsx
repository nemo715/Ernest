"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  deleteSession,
  getAgents,
  getSession,
  listSessions,
  streamApprove,
  streamChat,
} from "@/lib/api";
import { ErnestWS } from "@/lib/api";
import type { WSStatus } from "@/lib/api";
import type {
  AgentInfo,
  ApprovalRequest,
  RunEvent,
  SessionInfo,
  ToolCall,
  ToolResult,
  Usage,
} from "@/lib/types";
import { MessageView } from "@/components/MessageView";

// ---------------------------------------------------------------------------
// Chat item model
// ---------------------------------------------------------------------------

export interface ToolView {
  id: string;
  name: string;
  arguments: unknown;
  result?: unknown;
  resultError?: string;
  running?: boolean;
}

export type ApprovalStatus = "pending" | "resolving" | "approved" | "rejected";

export interface ApprovalView {
  req: ApprovalRequest;
  status: ApprovalStatus;
}

export type ChatItem =
  | { kind: "user"; id: string; text: string }
  | {
      kind: "assistant";
      id: string;
      text: string;
      tools: ToolView[];
      streaming: boolean;
      usage?: Usage;
    }
  | { kind: "approval"; id: string; approval: ApprovalView }
  | { kind: "step"; id: string; name: string }
  | { kind: "error"; id: string; text: string };

let counter = 0;
function uid(): string {
  counter += 1;
  return `it${Date.now().toString(36)}-${counter}`;
}

function lastAssistant(items: ChatItem[], id?: string | null): Extract<ChatItem, { kind: "assistant" }> | null {
  if (id) {
    const hit = items.find(
      (it): it is Extract<ChatItem, { kind: "assistant" }> =>
        it.kind === "assistant" && it.id === id,
    );
    if (hit) return hit;
  }
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i];
    if (it.kind === "assistant") return it;
  }
  return null;
}

function attachTool(
  item: Extract<ChatItem, { kind: "assistant" }>,
  tc: ToolCall,
  running = true,
): void {
  if (item.tools.some((t) => t.id === tc.id)) return;
  item.tools.push({
    id: tc.id,
    name: tc.name,
    arguments: tc.arguments,
    running,
  });
}

/** Finds an assistant item that already owns a tool call with the given id. */
function findToolOwner(
  items: ChatItem[],
  id: string,
): { item: Extract<ChatItem, { kind: "assistant" }>; tool: ToolView } | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i];
    if (it.kind !== "assistant") continue;
    const tool = it.tools.find((t) => t.id === id);
    if (tool) return { item: it, tool };
  }
  return null;
}

function toolResultOf(res: ToolResult): { result?: unknown; resultError?: string } {
  if (res.error) return { resultError: res.error };
  return { result: res.content };
}

function textOfMessage(m: { content?: string; parts?: { type: string; text?: string }[] }): string {
  if (m.content) return m.content;
  return (m.parts ?? [])
    .filter((p) => p.type === "text" && p.text)
    .map((p) => p.text)
    .join("");
}

function pretty(v: unknown): string {
  if (typeof v === "string") {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// ---------------------------------------------------------------------------
// Playground
// ---------------------------------------------------------------------------

export function Playground() {
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [agent, setAgent] = useState("");
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [items, setItems] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [streaming, setStreaming] = useState(false);
  const [busyApproval, setBusyApproval] = useState<string | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [wsStatus, setWsStatus] = useState<WSStatus>("connecting");
  const [steerText, setSteerText] = useState("");

  const itemsRef = useRef<ChatItem[]>([]);
  const activeAssistantId = useRef<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const wsRef = useRef<ErnestWS | null>(null);
  const runDoneRef = useRef<{ resolve: () => void } | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  const commit = useCallback((items: ChatItem[]) => {
    itemsRef.current = items;
    setItems([...items]);
  }, []);

  const refreshSessions = useCallback(() => {
    listSessions(agent || undefined)
      .then(setSessions)
      .catch(() => undefined);
  }, [agent]);

  useEffect(() => {
    getAgents()
      .then((ags) => {
        setAgents(ags);
        if (ags.length > 0) setAgent(ags[0].name);
      })
      .catch((e: unknown) => setError(`cannot reach ernest backend: ${String(e)}`));
  }, []);

  useEffect(() => {
    if (agent) refreshSessions();
  }, [agent, refreshSessions]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [items]);

  // -------------------------------------------------------------------------
  // Event handling
  // -------------------------------------------------------------------------

  /** Resolves when the current run finishes (run.complete / run.error). */
  const waitForRunEnd = useCallback((): Promise<void> => {
    runDoneRef.current?.resolve(); // release any stale waiter
    return new Promise<void>((resolve) => {
      runDoneRef.current = { resolve };
    });
  }, []);

  const signalRunEnd = useCallback(() => {
    const d = runDoneRef.current;
    runDoneRef.current = null;
    d?.resolve();
  }, []);

  const handleEvent = useCallback(
    (ev: RunEvent) => {
      const items = itemsRef.current;
      switch (ev.type) {
        case "message.delta": {
          const item = lastAssistant(items, activeAssistantId.current);
          if (item) item.text += ev.delta ?? "";
          break;
        }
        case "message.complete": {
          const item = lastAssistant(items, activeAssistantId.current);
          if (item) {
            item.streaming = false;
            if (!item.text && ev.message?.content) {
              item.text = ev.message.content;
            }
            for (const tc of ev.message?.toolCalls ?? []) attachTool(item, tc, false);
          } else if (ev.message) {
            const fresh = {
              kind: "assistant" as const,
              id: uid(),
              text: textOfMessage(ev.message),
              tools: [] as ToolView[],
              streaming: false,
            };
            for (const tc of ev.message.toolCalls ?? []) attachTool(fresh, tc, false);
            items.push(fresh);
          }
          break;
        }
        case "tool.call": {
          if (!ev.toolCall) break;
          // Resume replay: the call may already be rendered from the
          // paused run — just flip it back to running.
          const owner = findToolOwner(items, ev.toolCall.id);
          if (owner) {
            owner.tool.running = true;
          } else {
            const item = lastAssistant(items, activeAssistantId.current);
            if (item) attachTool(item, ev.toolCall, true);
          }
          break;
        }
        case "tool.result": {
          if (!ev.toolResult) break;
          const owner = findToolOwner(items, ev.toolResult.id);
          if (owner) {
            owner.tool.running = false;
            const { result, resultError } = toolResultOf(ev.toolResult);
            if (result !== undefined) owner.tool.result = result;
            if (resultError !== undefined) owner.tool.resultError = resultError;
          }
          break;
        }
        case "approval.requested": {
          if (!ev.approval) break;
          items.push({
            kind: "approval",
            id: uid(),
            approval: { req: ev.approval, status: "pending" },
          });
          break;
        }
        case "approval.resolved": {
          if (!ev.approval) break;
          for (const it of items) {
            if (it.kind === "approval" && it.approval.req.id === ev.approval.id) {
              it.approval.status =
                ev.approval.status === "approved" ? "approved" : "rejected";
              it.approval.req = ev.approval;
            }
          }
          break;
        }
        case "step.start": {
          if (!ev.step) break;
          const last = items[items.length - 1];
          if (last?.kind === "step" && last.name === ev.step) break;
          items.push({ kind: "step", id: uid(), name: ev.step });
          break;
        }
        case "run.error": {
          items.push({
            kind: "error",
            id: uid(),
            text: ev.error || "run failed",
          });
          const item = lastAssistant(items, activeAssistantId.current);
          if (item) item.streaming = false;
          signalRunEnd();
          break;
        }
        case "run.complete": {
          const item = lastAssistant(items, activeAssistantId.current);
          if (item) {
            item.streaming = false;
            if (ev.result?.usage) item.usage = ev.result.usage;
          }
          const sid = ev.result?.metadata?.sessionId;
          if (typeof sid === "string" && sid) setSessionId(sid);
          if (ev.result?.status !== "awaiting_approval") {
            refreshSessions();
          }
          signalRunEnd();
          break;
        }
        default:
          break;
      }
      commit(items);
    },
    [commit, refreshSessions, signalRunEnd],
  );

  // One persistent /ws/chat connection; the client falls back to SSE
  // (streamChat/streamApprove) when no server exposes the socket.
  useEffect(() => {
    let ws: ErnestWS | null = null;
    let ping: ReturnType<typeof setInterval> | null = null;
    let cancelled = false;
    void (async () => {
      ws = new ErnestWS({ onEvent: handleEvent, onStatus: setWsStatus });
      wsRef.current = ws;
      const ok = await ws.connect();
      if (cancelled) return;
      if (ok) ping = setInterval(() => ws?.ping(), 30_000);
    })();
    return () => {
      cancelled = true;
      if (ping) clearInterval(ping);
      ws?.close();
      wsRef.current = null;
    };
  }, [handleEvent]);

  // -------------------------------------------------------------------------
  // Actions
  // -------------------------------------------------------------------------

  const newChat = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    wsRef.current?.interrupt();
    commit([]);
    activeAssistantId.current = null;
    setSessionId(null);
    setError(null);
  }, [commit]);

  const loadSession = useCallback(
    async (id: string) => {
      try {
        const sess = await getSession(id);
        const out: ChatItem[] = [];
        for (const m of sess.messages) {
          if (m.role === "user") {
            out.push({ kind: "user", id: uid(), text: textOfMessage(m) });
          } else if (m.role === "assistant") {
            const item: Extract<ChatItem, { kind: "assistant" }> = {
              kind: "assistant",
              id: uid(),
              text: textOfMessage(m),
              tools: [],
              streaming: false,
            };
            for (const tc of m.toolCalls ?? []) attachTool(item, tc, false);
            out.push(item);
          } else if (m.role === "tool") {
            const target = [...out].reverse().find(
              (it): it is Extract<ChatItem, { kind: "assistant" }> =>
                it.kind === "assistant" && it.tools.some((t) => t.id === m.toolCallID),
            );
            if (target) {
              const tool = target.tools.find((t) => t.id === m.toolCallID);
              if (tool) {
                try {
                  tool.result = JSON.parse(m.content ?? "{}");
                } catch {
                  tool.result = m.content;
                }
                tool.running = false;
              }
            }
          }
        }
        for (const ap of sess.pendingApprovals ?? []) {
          out.push({
            kind: "approval",
            id: uid(),
            approval: { req: ap, status: "pending" },
          });
        }
        commit(out);
        setSessionId(sess.id);
        setError(null);
      } catch (e: unknown) {
        setError(`load session: ${String(e)}`);
      }
    },
    [commit],
  );

  const removeSession = useCallback(
    async (id: string, e: React.MouseEvent) => {
      e.stopPropagation();
      try {
        await deleteSession(id);
        if (sessionId === id) {
          commit([]);
          setSessionId(null);
        }
        refreshSessions();
      } catch (err: unknown) {
        setError(`delete session: ${String(err)}`);
      }
    },
    [commit, refreshSessions, sessionId],
  );

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || !agent || streaming) return;
    setInput("");
    setError(null);

    const items = itemsRef.current;
    items.push({ kind: "user", id: uid(), text });
    const aid = uid();
    activeAssistantId.current = aid;
    items.push({
      kind: "assistant",
      id: aid,
      text: "",
      tools: [],
      streaming: true,
    });
    commit(items);
    setStreaming(true);

    const ac = new AbortController();
    abortRef.current = ac;
    try {
      if (wsRef.current?.open) {
        wsRef.current.chat({ agent, input: text, sessionId: sessionId ?? undefined });
        await waitForRunEnd();
      } else {
        await streamChat(
          { agent, input: text, sessionId: sessionId ?? undefined },
          handleEvent,
          ac.signal,
        );
      }
    } catch (e: unknown) {
      const aborted = e instanceof DOMException && e.name === "AbortError";
      if (!aborted) {
        const items2 = itemsRef.current;
        items2.push({ kind: "error", id: uid(), text: `request failed: ${String(e)}` });
        commit(items2);
      }
    } finally {
      const items3 = itemsRef.current;
      const item = lastAssistant(items3, activeAssistantId.current);
      if (item) item.streaming = false;
      commit(items3);
      setStreaming(false);
      activeAssistantId.current = null;
      refreshSessions();
    }
  }, [agent, commit, handleEvent, input, refreshSessions, sessionId, streaming, waitForRunEnd]);

  const decide = useCallback(
    async (ap: ApprovalRequest, approved: boolean, note: string) => {
      if (busyApproval || !ap.agentName) return;
      setBusyApproval(ap.id);
      setError(null);
      // Mark the card resolving immediately.
      const items0 = itemsRef.current;
      for (const it of items0) {
        if (it.kind === "approval" && it.approval.req.id === ap.id) {
          it.approval.status = "resolving";
        }
      }
      commit(items0);

      const items = itemsRef.current;
      const aid = uid();
      activeAssistantId.current = aid;
      items.push({
        kind: "assistant",
        id: aid,
        text: "",
        tools: [],
        streaming: true,
      });
      commit(items);

      const ac = new AbortController();
      abortRef.current = ac;
      try {
        if (wsRef.current?.open) {
          wsRef.current.approve({
            agent: ap.agentName,
            approvalId: ap.id,
            approved,
            note: note || undefined,
          });
          await waitForRunEnd();
        } else {
          await streamApprove(
            { agent: ap.agentName, approvalId: ap.id, approved, note: note || undefined },
            handleEvent,
            ac.signal,
          );
        }
      } catch (e: unknown) {
        const aborted = e instanceof DOMException && e.name === "AbortError";
        if (!aborted) {
          const items2 = itemsRef.current;
          items2.push({ kind: "error", id: uid(), text: `approval failed: ${String(e)}` });
          commit(items2);
        }
      } finally {
        const items3 = itemsRef.current;
        const item = lastAssistant(items3, activeAssistantId.current);
        if (item) item.streaming = false;
        // If the server never emitted approval.resolved (e.g. the resume
        // stream died), put the card back into a decidable state.
        for (const it of items3) {
          if (it.kind === "approval" && it.approval.req.id === ap.id && it.approval.status === "resolving") {
            it.approval.status = "pending";
          }
        }
        commit(items3);
        setBusyApproval(null);
        setStreaming(false);
        activeAssistantId.current = null;
        refreshSessions();
      }
    },
    [busyApproval, commit, handleEvent, refreshSessions, waitForRunEnd],
  );

  const interrupt = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    wsRef.current?.interrupt();
  }, []);

  const sendSteer = useCallback(() => {
    const text = steerText.trim();
    if (!text) return;
    wsRef.current?.steer(text);
    setSteerText("");
  }, [steerText]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  const currentAgent = agents.find((a) => a.name === agent);

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <span className="logo">✦</span> ernest
        </div>

        <label className="field-label" htmlFor="agent-select">
          Agent
        </label>
        <select
          id="agent-select"
          className="agent-select"
          value={agent}
          onChange={(e) => {
            setAgent(e.target.value);
            newChat();
          }}
        >
          {agents.length === 0 && <option value="">loading…</option>}
          {agents.map((a) => (
            <option key={a.name} value={a.name}>
              {a.name}
            </option>
          ))}
        </select>
        {currentAgent?.description && (
          <p className="agent-desc">{currentAgent.description}</p>
        )}

        <button className="new-chat" onClick={newChat}>
          ＋ New chat
        </button>

        <div className="sessions">
          <h3>Sessions</h3>
          {sessions.length === 0 && (
            <p className="sessions-empty">no sessions yet</p>
          )}
          {sessions.map((s) => (
            <div
              key={s.id}
              className={`session ${s.id === sessionId ? "active" : ""}`}
              onClick={() => void loadSession(s.id)}
            >
              <div className="session-top">
                <span className="session-id">{s.id.slice(0, 8)}</span>
                <button
                  className="session-del"
                  title="delete session"
                  onClick={(e) => void removeSession(s.id, e)}
                >
                  ✕
                </button>
              </div>
              <div className="session-meta">
                {s.agentName} · {s.messages} msg
                {s.pendingApprovals > 0 ? ` · ${s.pendingApprovals} approval(s)` : ""}
              </div>
            </div>
          ))}
        </div>

        <div className="sidebar-foot">
          {currentAgent && (
            <>
              <span className="meta">{currentAgent.provider}</span>
              <span className="meta">{currentAgent.model}</span>
              <span className="meta">{currentAgent.tools.length} tool(s)</span>
            </>
          )}
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <select
            className="agent-select mobile"
            aria-label="agent"
            value={agent}
            onChange={(e) => {
              setAgent(e.target.value);
              newChat();
            }}
          >
            {agents.map((a) => (
              <option key={a.name} value={a.name}>
                {a.name}
              </option>
            ))}
          </select>
          <span className="agent-name">{agent || "ernest"}</span>
          {currentAgent && <span className="agent-model">{currentAgent.model}</span>}
          <span className={`pill ${wsStatus === "open" ? "live" : ""}`}>
            <span className="dot" />
            {wsStatus === "open" ? "ws" : wsStatus === "connecting" ? "connecting…" : "sse"}
          </span>
          {streaming && (
            <span className="pill live">
              <span className="dot" /> streaming
            </span>
          )}
          <button className="new-chat mobile" onClick={newChat} title="new chat">
            ＋
          </button>
        </header>

        <div className="messages">
          {items.length === 0 && (
            <div className="empty">
              <div className="empty-logo">✦</div>
              <h2>{agent || "ernest"} playground</h2>
              <p>
                Send a message to start a run. Tool calls stream live; actions
                that require approval pause the run until you decide.
              </p>
            </div>
          )}
          {items.map((it) => (
            <MessageView
              key={it.id}
              item={it}
              streaming={streaming}
              busyApproval={busyApproval}
              onDecide={decide}
            />
          ))}
          <div ref={bottomRef} />
        </div>

        {error && (
          <div className="errorbar">
            <span>{error}</span>
            <button onClick={() => setError(null)}>✕</button>
          </div>
        )}

        <footer className="composer">
          {streaming && (
            <>
              <button
                className="stop"
                onClick={interrupt}
                title="interrupt the running generation"
              >
                ■ Interrupt
              </button>
              {wsStatus === "open" && (
                <input
                  className="steer"
                  value={steerText}
                  onChange={(e) => setSteerText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      sendSteer();
                    }
                  }}
                  placeholder="Redirect the run…"
                />
              )}
            </>
          )}
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={
              streaming
                ? "run in progress…"
                : "Message the agent (Enter to send, Shift+Enter for newline)"
            }
            rows={Math.min(6, input.split("\n").length)}
            disabled={streaming}
          />
          <button
            className="send"
            onClick={() => void send()}
            disabled={!agent || streaming || !input.trim()}
          >
            Send
          </button>
        </footer>
      </main>
    </div>
  );
}

export { pretty };
