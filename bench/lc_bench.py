"""LangChain framework-overhead benchmark with a fake LLM (no network,
no API keys) - the fair counterpart of ernest's mock-provider bench.

Run: bench\\venv\\Scripts\\python.exe bench\\lc_bench.py
"""
import statistics
import sys
import time

from langchain_core.language_models.fake_chat_models import FakeListChatModel
from langchain_core.prompts import ChatPromptTemplate

N = 200
W = 20

llm = FakeListChatModel(responses=["Hello from langchain"])
chain = ChatPromptTemplate.from_messages(
    [("system", "You are a helpful assistant."), ("human", "{input}")]
) | llm


def bench(name, fn):
    for _ in range(W):
        fn()
    ts = []
    for _ in range(N):
        a = time.perf_counter()
        fn()
        ts.append(time.perf_counter() - a)
    ts.sort()
    mean = statistics.mean(ts) * 1e6
    p = lambda x: ts[int(x * (N - 1))] * 1e6
    print(
        f"{name:<44s} mean {mean:8.1f} µs   p50 {p(0.5):7.1f} µs   "
        f"p95 {p(0.95):7.1f} µs   {N / sum(ts):6.0f} turns/s"
    )


bench("langchain raw FakeListChatModel.invoke", lambda: llm.invoke("hi"))
bench("langchain chain invoke (prompt | llm)", lambda: chain.invoke({"input": "hi"}))
print(f"modules loaded after imports: {len(sys.modules)}")
