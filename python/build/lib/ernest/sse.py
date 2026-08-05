"""Minimal SSE parser for ernest's ``data: {json}\\n\\n`` event frames.

The Go server (``internal/server/server.go``) and the mock backend write
one JSON payload per frame with no ``event:``/``id:`` fields, so a small
parser is enough. Both a sync iterator (for :class:`ernest.Client`) and an
async iterator (for :class:`ernest.AsyncClient`) are provided; they share
the same frame-splitting logic via :func:`feed_line`.
"""

from __future__ import annotations

import json
from typing import Any, AsyncIterator, Awaitable, Callable, Iterator, List, Optional

from .errors import SSEProtocolError

# read_line() -> bytes line (with or without newline) or None at EOF.
LineReader = Callable[[], Optional[bytes]]
AsyncLineReader = Callable[[], Awaitable[Optional[bytes]]]


def _decode(raw: bytes) -> str:
    return raw.decode("utf-8", "replace")


def feed_line(data_lines: List[str], line: str) -> Optional[dict]:
    """Process one decoded SSE line. Returns a parsed event when the frame
    is complete (blank line after one or more ``data:`` lines)."""
    if line.startswith("data:"):
        data_lines.append(line[5:].lstrip(" "))
    elif line == "" and data_lines:
        payload = "\n".join(data_lines)
        data_lines.clear()
        return _parse(payload)
    # Ignore comments, event:, id:, retry:.
    return None


def _parse(payload: str) -> dict:
    try:
        return json.loads(payload)
    except json.JSONDecodeError as exc:  # pragma: no cover - defensive
        raise SSEProtocolError(f"invalid SSE payload: {exc}") from exc


def iter_sse_json(read_line: LineReader) -> Iterator[dict]:
    """Yield JSON payloads from a sync SSE stream.

    ``read_line`` must return the next ``bytes`` line (with or without
    newline) or ``None`` at EOF.
    """
    data_lines: List[str] = []
    while True:
        raw = read_line()
        if raw is None or raw == b"":
            break
        event = feed_line(data_lines, _decode(raw).rstrip("\r\n"))
        if event is not None:
            yield event


async def aiter_sse_json(read_line: AsyncLineReader) -> AsyncIterator[dict]:
    """Yield JSON payloads from an async SSE stream (asyncio)."""
    data_lines: List[str] = []
    while True:
        raw = await read_line()
        if raw is None or raw == b"":
            break
        event = feed_line(data_lines, _decode(raw).rstrip("\r\n"))
        if event is not None:
            yield event
