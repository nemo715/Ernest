#!/usr/bin/env python3
"""Example: typed error handling in the ernest SDK.

Every failure maps to a typed exception:
  - HTTP failures   -> APIError subclasses (BadRequestError, NotFoundError,
                       RateLimitError, ServerError)
  - run.error event -> RunError subclasses (ProviderError, ToolError,
                       AgentError, ValidationError, RunTimeout, ...);
                       raised automatically by client.stream_chat/chat/approve
  - malformed SSE   -> SSEProtocolError

Run against a running ernest server (or the web playground mock):

    npm --prefix web run mock        # terminal 1: mock backend on :9090
    python examples/python/errors.py
"""

import os
import sys

# Make the SDK importable without installation: this repo keeps the
# package under python/ernest, so add it to sys.path when running the
# example in-place. Installed SDKs do not need this line.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "python"))

from ernest import (
    APIError,
    AgentError,
    Client,
    NotFoundError,
    RunError,
    RunEvent,
    error_from_event,
)

BASE_URL = os.environ.get("ERNEST_URL", "http://127.0.0.1:9090")


def main() -> int:
    client = Client(BASE_URL)

    # 1. HTTP-level errors: 404 for a session that does not exist.
    try:
        client.get_session("does-not-exist")
        print("unexpected: got a session")
        return 1
    except NotFoundError as exc:
        print(f"NotFoundError: status={exc.status} message={exc.message!r}")
    except APIError as exc:
        print(f"APIError: status={exc.status} message={exc.message!r}")

    # 2. run.error events: the client raises the matching typed error
    #    automatically (the pytest mock server triggers them with the
    #    inputs "boom", "tool-boom", "provider-boom"). The mapping itself
    #    is exposed as error_from_event, e.g. for custom stream handling:
    err = error_from_event(RunEvent.from_dict({"type": "run.error", "error": "agent_error: crashed", "runId": "r1", "agent": "a"}))
    assert isinstance(err, AgentError) and err.run_id == "r1"
    print(f"error_from_event -> {type(err).__name__} kind={err.kind!r} run_id={err.run_id!r}")
    err = error_from_event(RunEvent.from_dict({"type": "run.error", "error": "tool_not_found: send_email", "runId": "r2", "agent": "a"}))
    print(f"error_from_event -> {type(err).__name__} message={err.message!r}")

    # 3. Catch hierarchy: every typed run error is a RunError subclass,
    #    so one handler covers all agent-side failures.
    try:
        client.chat("assistant", "provider-boom")  # -> ProviderError on the test mock
    except RunError as exc:
        print(f"RunError fallback: [{exc.kind}] {exc.message} (run_id={exc.run_id!r})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
