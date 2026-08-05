#!/usr/bin/env python3
"""Example: async streaming chat with the ernest SDK.

Run against a running ernest server (or the web playground mock):

    npm --prefix web run mock        # terminal 1: mock backend on :9090
    python examples/python/stream_async.py
"""

import asyncio
import os
import sys

# Make the SDK importable without installation: this repo keeps the
# package under python/ernest, so add it to sys.path when running the
# example in-place. Installed SDKs do not need this line.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "python"))

from ernest import (
    AsyncClient,
    EVENT_APPROVAL_REQUESTED,
    EVENT_MESSAGE_DELTA,
    EVENT_RUN_COMPLETE,
    EVENT_TOOL_CALL,
    RunError,
)

BASE_URL = os.environ.get("ERNEST_URL", "http://127.0.0.1:9090")


async def main() -> None:
    client = AsyncClient(BASE_URL)

    # Stream one turn; every event is delivered as it arrives.
    print(">>> async stream_chat")
    async for event in client.stream_chat("assistant", "hello"):
        if event.type == EVENT_MESSAGE_DELTA:
            print(event.delta, end="", flush=True)
        elif event.type == EVENT_TOOL_CALL:
            print(f"\n[tool call] {event.tool_call.name}({event.tool_call.arguments})")
        elif event.type == EVENT_APPROVAL_REQUESTED:
            print(f"\n[approval] {event.approval.id}: {event.approval.summary}")
        elif event.type == EVENT_RUN_COMPLETE:
            result = event.result
    print()

    # Approve the pending action, if any.
    if result.awaiting_approval:
        approval = result.approvals[0]
        print(f">>> async approve {approval.id}")
        result = await client.approve("assistant", approval.id, True)
    print(f"status={result.status} messages={len(result.messages)}")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except RunError as exc:
        print(f"\nerror: [{exc.kind}] {exc.message}", file=sys.stderr)
        sys.exit(1)
