# ernest — user flows

End-to-end flows for the CLI, the HTTP/SSE API, and the web playground.
Wire format reference: `internal/core/events.go` + `types.go` (mirrored in
`web/lib/types.ts`).

## 1. CLI

```
ernest init            # scaffold ernest.yaml + agents.yaml in the current dir
ernest new <template>  # scaffold agent/team/workflow/server (ernest.json + main.go)
ernest run "hello"     # one-shot chat (--json for machine-readable RunEvents)
ernest playground      # serve API (9090) + static UI (8080, --static web/out)
ernest playground --demo  # self-contained mock-agent server, no ernest.json needed
ernest doctor          # check config, providers, storage connectivity
ernest eval <dir>      # run scenario evals (asserts, samples, iterations)
ernest mcp-serve       # expose agents as MCP tools (stdio or SSE)
```

## 2. HTTP API (playground backend)

| Method | Path | Purpose |
|---|---|---|
| GET  | `/api/agents` | list agents |
| GET  | `/api/sessions` | list sessions |
| GET  | `/api/sessions/:id` | session detail + message history |
| DELETE | `/api/sessions/:id` | delete session |
| GET  | `/api/runs/:id/trace` | spans + metrics of a finished run |
| POST | `/api/chat` | start a run; SSE stream of `RunEvent`s |
| POST | `/api/approve` | resume a paused run (approval decision) |
| WS   | `/ws/chat` | duplex transport: `chat|steer|interrupt|approve|ping` → `ready|pong|ack|error` + `RunEvent`s |

## 3. Chat + streaming flow

1. Client sends `POST /api/chat` with `{agent, input, session, memory}`.
2. Server responds `text/event-stream`; each frame is
   `data: {json}\n\n` with a `RunEvent`.
3. Event sequence for a normal run:

```
run.start            → chat item appears, empty
message.delta × n    → tokens appended (assistant bubble types)
tool.call            → tool card appears (running…)
tool.result          → tool card resolves (result or error)
message.complete     → bubble finalized
step.start / step.end
trace.span           → instrumented span (llm / tool / step / approval)
run.metrics          → iterations, tokens, cost, duration, status
run.complete         → usage tokens + status
```

4. The same frames flow over `/ws/chat` when the UI is on the WebSocket
   transport: Interrupt sends `{type:"interrupt"}` (the run unwinds with
   `run.error`), and a steer sends `{type:"steer", input}` which cancels
   the current generation and queues a follow-up run on the same session.
   The SSE path stops the stream by aborting the `fetch`.

## 4. Human-in-the-loop (approvals)

1. Agent hits a tool marked `require_approval` → run pauses
   (`status: awaiting_approval`), server emits `approval.requested`.
2. UI shows the approval card (PENDING) with the tool args and a note input.
3. User clicks **Approve** or **Reject** (optional note) →
   `POST /api/approve` with `{run_id, approval_id, decision, note}`.
4. Server resumes the run via `StreamResume`; the blocked tool call is
   replayed with the same call id and emits:

```
approval.resolved   → card flips to APPROVED / REJECTED
tool.result         → matches the original tool.call id
run.complete        → run finishes
```

5. Rejecting produces a tool error `tool call rejected by human` and the
   agent answers with a "skipped" reply.

## 5. Web playground

```
npm run dev                     # next dev on :3000 (hot reload)
npm run mock                    # mock backend on 127.0.0.1:9090 (no Go needed)
npm run build                   # static export → web/out/
node scripts/serve-static.mjs   # host web/out on :8080
```

Backend resolution (`web/lib/api.ts`): same-origin first, then
`http://127.0.0.1:9090` (override at build time with
`NEXT_PUBLIC_ERNEST_API_URL`). A non-JSON 404 or network error means "not
the ernest API" → next base. CORS is open (`*`) on the Go server for
cross-origin dev.

Flows covered in the UI:

- **Chat**: send message → streaming tokens → completion.
- **Tools**: cards with args/result JSON, expandable, per-call lifecycle.
- **Approvals**: approve/reject with note; resume replay keeps card state.
- **Sessions**: sidebar list, resume a past session, delete.
- **Agents**: dropdown (fetched from `/api/agents`).
- **Interrupt / Steer**: while streaming on WS, stop the run or redirect
  it; the top bar pill shows `ws` / `sse` / `connecting…`.
- **Mobile**: <760px hides the sidebar; agent select + new-chat move to the
  top bar.
