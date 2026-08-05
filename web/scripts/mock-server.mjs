// Mock ernest backend for UI development without a Go toolchain.
//
// Replicates the wire contract of internal/server/server.go:
//   GET  /healthz
//   GET  /api/agents
//   POST /api/chat     -> SSE run events
//   POST /api/approve  -> SSE resumed run events
//   GET  /api/sessions
//   GET  /api/sessions/{id}
//   DELETE /api/sessions/{id}
//
// The chat flow demonstrates: token streaming, a tool call, an approval
// pause (HITL), then resume after the decision.
//
// Usage: npm run mock   (listens on 127.0.0.1:9090)

import http from "node:http";

const PORT = 9090;
const AGENT = {
  name: "assistant",
  description: "Mock agent for UI development",
  model: "mock-1",
  provider: "mock",
  tools: ["calculator", "send_email", "now"],
};

const sessions = new Map();
// approval id -> session id (mirrors agent.registerApproval in Go).
const approvalToSession = new Map();

function uid(prefix) {
  return `${prefix}_${Math.random().toString(36).slice(2, 10)}`;
}

function cors(res) {
  res.setHeader("Access-Control-Allow-Origin", "*");
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "Content-Type");
}

function json(res, status, body) {
  cors(res);
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => {
      try {
        resolve(data ? JSON.parse(data) : {});
      } catch (e) {
        reject(e);
      }
    });
    req.on("error", reject);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function sseEvent(res, ev) {
  res.write(`data: ${JSON.stringify(ev)}\n\n`);
}

function beginSSE(res) {
  cors(res);
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
  });
}

// Emit a streamed reply: tokens, then (optionally) a tool call + approval
// request, then completion. On approval.resume the tool executes.
async function runChat(res, req, body, resumeApproval) {
  const runId = uid("run");
  const sessionId = body.sessionId || uid("session");
  const agentName = body.agent || AGENT.name;

  let session = sessions.get(sessionId);
  if (!session) {
    session = {
      id: sessionId,
      agentName,
      messages: [],
      pendingApprovals: [],
      pendingCalls: [],
    };
    sessions.set(sessionId, session);
  }

  if (!resumeApproval) {
    session.messages.push({
      role: "user",
      content: body.input,
      createdAt: new Date().toISOString(),
    });
  }

  beginSSE(res);
  sseEvent(res, { type: "run.start", runId, agent: agentName, data: { input: body.input ?? "" } });

  const reply = resumeApproval
    ? `Done — I sent the email${resumeApproval.approved ? "" : "? No, rejected"}. ${resumeApproval.approved ? "The tool executed successfully." : "The tool was skipped per your decision."}`
    : "Sure — let me compute that and, if you want, I can email you the result.";

  // Stream tokens.
  for (let i = 0; i < reply.length; i += 2) {
    sseEvent(res, { type: "message.delta", runId, agent: agentName, delta: reply.slice(i, i + 2) });
    await sleep(24);
  }
  sseEvent(res, { type: "message.complete", runId, agent: agentName, message: { role: "assistant", content: reply, createdAt: new Date().toISOString() } });

  if (!resumeApproval) {
    // Tool call that requires HITL approval.
    const call = { id: uid("call"), name: "send_email", arguments: { to: "team@example.com", subject: "Ernest demo", body: "Automated summary" } };
    sseEvent(res, { type: "tool.call", runId, agent: agentName, toolCall: call });

    const approval = {
      id: uid("ap"),
      runId,
      agentName,
      action: "send_email",
      summary: `Send an email to team@example.com with subject "Ernest demo"?`,
      context: { to: "team@example.com", subject: "Ernest demo" },
      status: "pending",
      createdAt: new Date().toISOString(),
    };
    session.pendingApprovals.push(approval);
    session.pendingCalls.push({ approvalId: approval.id, call });
    approvalToSession.set(approval.id, sessionId);
    sseEvent(res, { type: "approval.requested", runId, agent: agentName, approval });

    const result = {
      runId,
      status: "awaiting_approval",
      output: reply,
      messages: session.messages,
      approvals: session.pendingApprovals,
      durationMs: 640,
      metadata: { agent: agentName, iterations: 1, sessionId },
    };
    sseEvent(res, { type: "run.complete", runId, agent: agentName, result });
  } else {
    const ap = session.pendingApprovals.find((a) => a.id === resumeApproval.id);
    if (ap) {
      ap.status = resumeApproval.approved ? "approved" : "rejected";
      ap.note = resumeApproval.note ?? "";
      ap.resolvedAt = new Date().toISOString();
      sseEvent(res, { type: "approval.resolved", runId, agent: agentName, approval: ap });
    }
    session.pendingApprovals = session.pendingApprovals.filter((a) => a.id !== resumeApproval.id);

    const blocked = session.pendingCalls.find((c) => c.approvalId === resumeApproval.id);
    session.pendingCalls = session.pendingCalls.filter((c) => c.approvalId !== resumeApproval.id);

    const toolResult = resumeApproval.approved
      ? { id: blocked.call.id, name: "send_email", content: { ok: true, to: "team@example.com" } }
      : { id: blocked.call.id, name: "send_email", content: {}, error: "tool call rejected by human", approvalRequired: true };
    sseEvent(res, { type: "tool.result", runId, agent: agentName, toolResult });
    session.messages.push({ role: "tool", name: "send_email", toolCallID: toolResult.id, content: JSON.stringify(toolResult), createdAt: new Date().toISOString() });

    const result = {
      runId,
      status: "completed",
      output: reply,
      messages: session.messages,
      durationMs: 980,
      usage: { inputTokens: 412, outputTokens: 96 },
      metadata: { agent: agentName, iterations: 2, sessionId },
    };
    sseEvent(res, { type: "run.complete", runId, agent: agentName, result });
  }
  res.end();
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const path = url.pathname;

  if (req.method === "OPTIONS") {
    cors(res);
    res.writeHead(204);
    res.end();
    return;
  }

  if (req.method === "GET" && path === "/healthz") {
    json(res, 200, { status: "ok", agents: 1 });
    return;
  }

  if (req.method === "GET" && path === "/api/agents") {
    json(res, 200, [AGENT]);
    return;
  }

  if (req.method === "POST" && path === "/api/chat") {
    const body = await readBody(req);
    await runChat(res, req, body, null);
    return;
  }

  if (req.method === "POST" && path === "/api/approve") {
    const body = await readBody(req);
    console.log("approve:", JSON.stringify(body));
    const sessionId = approvalToSession.get(body.approvalId) ?? uid("session");
    await runChat(res, req, { sessionId }, {
      id: body.approvalId,
      approved: body.approved,
      note: body.note,
    });
    return;
  }

  if (req.method === "GET" && path === "/api/sessions") {
    const list = [...sessions.entries()].map(([id, s]) => ({
      id,
      agentName: s.agentName,
      messages: s.messages.length,
      pendingApprovals: s.pendingApprovals.length,
      updatedAt: new Date().toISOString(),
    }));
    json(res, 200, list);
    return;
  }

  if (req.method === "GET" && path.startsWith("/api/sessions/")) {
    const id = decodeURIComponent(path.slice("/api/sessions/".length));
    const s = sessions.get(id);
    if (!s) {
      json(res, 404, { error: `session ${id} not found` });
      return;
    }
    json(res, 200, s);
    return;
  }

  if (req.method === "DELETE" && path.startsWith("/api/sessions/")) {
    const id = decodeURIComponent(path.slice("/api/sessions/".length));
    sessions.delete(id);
    json(res, 200, { deleted: id });
    return;
  }

  json(res, 404, { error: `no route: ${req.method} ${path}` });
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`mock ernest backend on http://127.0.0.1:${PORT}`);
  console.log("flow: streaming tokens -> send_email tool -> HITL approval -> resume");
});
