#!/usr/bin/env python3
"""Example: stream a chat, answer a human-in-the-loop approval, and
inspect the session — using the sync ernest SDK.

Run against a running ernest server (or the web playground mock):

    npm --prefix web run mock        # terminal 1: mock backend on :9090
    python examples/python/chat.py

Exit code 0 on success, 1 on a typed ernest error.
"""

import os
import sys

# Make the SDK importable without installation: this repo keeps the
# package under python/ernest, so add it to sys.path when running the
# example in-place. Installed SDKs do not need this line.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "python"))

from ernest import (
    Client,
    EVENT_APPROVAL_REQUESTED,
    EVENT_MESSAGE_DELTA,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    RunError,
    Session,
)

BASE_URL = os.environ.get("ERNEST_URL", "http://127.0.0.1:9090")


def main() -> int:
    client = Client(BASE_URL)

    # 1. Stream a chat turn. Deltas arrive live; the run pauses with
    #    approval.requested when a tool wants human sign-off.
    print(">>> stream_chat")
    result = None
    for event in client.stream_chat("assistant", "hello", session_id="example-1"):
        if event.type == EVENT_MESSAGE_DELTA:
            print(event.delta, end="", flush=True)
        elif event.type == EVENT_TOOL_CALL:
            print(f"\n[tool call] {event.tool_call.name}({event.tool_call.arguments})")
        elif event.type == EVENT_TOOL_RESULT:
            print(f"[tool result] {event.tool_result.content}")
        elif event.type == EVENT_APPROVAL_REQUESTED:
            print(f"[approval] {event.approval.id}: {event.approval.summary}")
        elif event.type == "run.complete":
            result = event.result
    print()

    # 2. HITL: approve the pending action (or use approve(id, False) to
    #    reject; the run then fails with a "rejected by human" error).
    if result is not None and result.awaiting_approval:
        approval = result.approvals[0]
        print(f">>> approve {approval.id}")
        resume = client.approve("assistant", approval.id, True, note="looks good")
        result = resume
    if result is not None:
        print(f"status={result.status} messages={len(result.messages)} "
              f"usage={result.usage} duration={result.duration_ms}ms")

    # 3. Sessions API: list, fetch, delete.
    print(">>> sessions")
    for session in client.list_sessions(agent="assistant"):
        print(f"- {session.id}: {session.messages} messages, {session.pending_approvals} pending")
    fetched: Session = client.get_session("example-1")
    print(f"got {fetched.id} ({fetched.agent_name}, {len(fetched.messages)} messages)")
    print(f"deleted: {client.delete_session('example-1')}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RunError as exc:
        print(f"\nerror: [{exc.kind}] {exc.message} (run_id={exc.run_id or '-'})", file=sys.stderr)
        raise SystemExit(1) from exc
    except Exception as exc:  # APIError, SSEProtocolError, network, ...
        print(f"\nerror: {type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
