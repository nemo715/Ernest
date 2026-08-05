"""LangChain real-model benchmark - same model, same endpoint, same prompt
as bench/main.go -real (openai/gpt-4o-mini via OpenRouter).

Run: $env:OPENROUTER_API_KEY=...; bench\\venv\\Scripts\\python.exe bench\\lc_real.py
"""
import os
import statistics
import time

from langchain_core.prompts import ChatPromptTemplate
from langchain_openai import ChatOpenAI

key = os.environ["OPENROUTER_API_KEY"]
llm = ChatOpenAI(
    model="openai/gpt-4o-mini",
    base_url="https://openrouter.ai/api/v1",
    api_key=key,
    request_timeout=120,
    max_retries=1,
)
chain = (
    ChatPromptTemplate.from_messages(
        [("system", "You are a helpful assistant."), ("human", "{input}")]
    )
    | llm
)
N, W = 20, 1


def bench(name, n, fn):
    for _ in range(W):
        fn()
    ts, inp, out = [], 0, 0
    for _ in range(n):
        a = time.perf_counter()
        r = fn()
        ts.append(time.perf_counter() - a)
        um = getattr(r, "usage_metadata", None) or {}
        inp += um.get("input_tokens", 0)
        out += um.get("output_tokens", 0)
    ts.sort()
    mean = statistics.mean(ts) * 1e3
    p = lambda x: ts[int(x * (n - 1))] * 1e3
    cost = (inp * 0.15 + out * 0.60) / 1e6
    print(
        f"{name:<46s} mean {mean:6.1f} ms  p50 {p(0.5):6.1f} ms  p95 {p(0.95):6.1f} ms  "
        f"tokens {inp}+{out}  est ${cost:.4f}"
    )


bench("langchain chain.invoke (real gpt-4o-mini)", N,
      lambda: chain.invoke({"input": "Reply with the single word: ok"}))

# Streaming: time to first non-empty content chunk.
ttfb = []
for _ in range(5):
    t0 = time.perf_counter()
    for c in chain.stream({"input": "Reply with the single word: ok"}):
        if c.content:
            ttfb.append(time.perf_counter() - t0)
            break
print(f"{'langchain chain.stream first-token (real, 5 turns)':<46s} mean {statistics.mean(ttfb) * 1e3:6.1f} ms first token")
