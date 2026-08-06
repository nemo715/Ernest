package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

// ---------------------------------------------------------------------------
// Fake HTTP server (speaks streamable-HTTP JSON-RPC)
// ---------------------------------------------------------------------------

type fakeHTTPServer struct {
	srv  *httptest.Server
	reqs atomic.Int32
}

func newFakeHTTPServer(t *testing.T, callMode string) *fakeHTTPServer {
	t.Helper()
	f := &fakeHTTPServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		n := f.reqs.Add(1)
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("request %d: missing SSE accept", n)
		}
		if r.Header.Get("MCP-Protocol-Version") == "" {
			t.Errorf("request %d: missing protocol version", n)
		}
		if n > 1 && r.Header.Get("Mcp-Session-Id") != "sess-1" {
			t.Errorf("request %d: missing session id, got %q", n, r.Header.Get("Mcp-Session-Id"))
		}
		if n == 1 {
			w.Header().Set("Mcp-Session-Id", "sess-1")
		}
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			writeJSON(w, rpcOut(req.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-http", "version": "1.0.0"},
			}))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, rpcOut(req.ID, map[string]any{"tools": []Tool{
				{Name: "fake_echo", Description: "Echo a message back", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`)},
			}}))
		case "tools/call":
			params := req.Params
			name, _ := params["name"].(string)
			if name != "fake_echo" {
				writeJSON(w, rpcErrOut(req.ID, -32602, "unknown tool "+name))
				return
			}
			args, _ := params["arguments"].(map[string]any)
			msg, _ := args["message"].(string)
			res := map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + msg}}}
			if callMode == "sse" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", mustJSON(rpcOut(req.ID, res)))
				return
			}
			writeJSON(w, rpcOut(req.ID, res))
		default:
			if req.ID != nil {
				writeJSON(w, rpcErrOut(req.ID, -32601, "method not found "+req.Method))
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func rpcOut(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcErrOut(id any, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(mustJSON(v)))
}

// ---------------------------------------------------------------------------
// HTTP client tests
// ---------------------------------------------------------------------------

func TestHTTPInitializeListAndCall(t *testing.T) {
	f := newFakeHTTPServer(t, "json")
	defer f.srv.Close()
	c, err := NewHTTP(f.srv.URL+"/mcp", Options{Name: "ernest-test", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "fake_echo" {
		t.Fatalf("tools = %+v", tools)
	}
	res, err := c.Call(ctx, "fake_echo", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "echo: hello" || res.IsError {
		t.Fatalf("result = %+v", res)
	}
	// initialize + initialized notification + tools/list + tools/call.
	if f.reqs.Load() != 4 {
		t.Fatalf("requests = %d, want 4", f.reqs.Load())
	}
}

func TestHTTPCallSSE(t *testing.T) {
	f := newFakeHTTPServer(t, "sse")
	defer f.srv.Close()
	c, err := NewHTTP(f.srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "fake_echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "echo: hi" {
		t.Fatalf("result = %+v", res)
	}
}

func TestHTTPRPCMethodError(t *testing.T) {
	f := newFakeHTTPServer(t, "json")
	defer f.srv.Close()
	c, err := NewHTTP(f.srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Call(context.Background(), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPGarbageResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()
	c, err := NewHTTP(srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected error for garbage response")
	}
}

func TestHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	c, err := NewHTTP(srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected error for 502")
	}
}

func TestHTTPInvalidEndpoint(t *testing.T) {
	if _, err := NewHTTP("", Options{}); err == nil {
		t.Fatal("empty endpoint must error")
	}
	if _, err := NewHTTP("relative/path", Options{}); err == nil {
		t.Fatal("endpoint without scheme must error")
	}
}

func TestHTTPCloseTerminatesSession(t *testing.T) {
	var deleted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		writeJSON(w, rpcOut(1, map[string]any{"protocolVersion": ProtocolVersion}))
	}))
	defer srv.Close()
	c, err := NewHTTP(srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if !deleted.Load() {
		t.Fatal("session delete not sent")
	}
}

// ---------------------------------------------------------------------------
// Fake stdio server (self re-exec pattern: the test binary becomes the
// MCP server when the env flag is set)
// ---------------------------------------------------------------------------

const stdioServerEnv = "ERNEST_MCP_STDIO_SERVER"

// TestStdioServerEntry re-runs this binary as a fake MCP server. It only
// acts when the env flag is set; otherwise it is a no-op.
func TestStdioServerEntry(t *testing.T) {
	if os.Getenv(stdioServerEnv) != "1" {
		return
	}
	if err := serveFakeStdioMCP(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func serveFakeStdioMCP(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return err
		}
		switch req.Method {
		case "initialize":
			if err := writeRPCLine(w, rpcOut(req.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-stdio", "version": "1.0.0"},
			})); err != nil {
				return err
			}
		case "notifications/initialized":
			// no response expected
		case "tools/list":
			if err := writeRPCLine(w, rpcOut(req.ID, map[string]any{"tools": []Tool{
				{Name: "fake_echo", Description: "Echo a message back", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`)},
				{Name: "fake_fail", Description: "Always fails", InputSchema: json.RawMessage(`{"type":"object"}`)},
			}})); err != nil {
				return err
			}
		case "tools/call":
			params := req.Params
			name, _ := params["name"].(string)
			switch name {
			case "fake_echo":
				args, _ := params["arguments"].(map[string]any)
				msg, _ := args["message"].(string)
				if err := writeRPCLine(w, rpcOut(req.ID, Result{Content: []ContentBlock{{Type: "text", Text: "echo: " + msg}}})); err != nil {
					return err
				}
			case "fake_fail":
				if err := writeRPCLine(w, rpcOut(req.ID, Result{IsError: true, Content: []ContentBlock{{Type: "text", Text: "boom"}}})); err != nil {
					return err
				}
			default:
				if err := writeRPCLine(w, rpcErrOut(req.ID, -32602, "unknown tool "+name)); err != nil {
					return err
				}
			}
		default:
			if req.ID != nil {
				if err := writeRPCLine(w, rpcErrOut(req.ID, -32601, "method not found "+req.Method)); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

func writeRPCLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func newStdioTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := NewStdio(os.Args[0], []string{"-test.run=TestStdioServerEntry"}, Options{Env: []string{stdioServerEnv + "=1"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ---------------------------------------------------------------------------
// stdio client tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// client resources + prompts
// ---------------------------------------------------------------------------

func TestClientResourcesAndPrompts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			writeJSON(w, rpcOut(req.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "resources/list":
			writeJSON(w, rpcOut(req.ID, map[string]any{"resources": []Resource{{
				URI: "docs://policy", Name: "Policy", Description: "Vendor policy", MimeType: "text/markdown",
			}}}))
		case "resources/read":
			writeJSON(w, rpcOut(req.ID, map[string]any{"contents": []ResourceContent{{
				URI: "docs://policy", MimeType: "text/markdown", Text: "Only Acme is approved.",
			}}}))
		case "prompts/list":
			writeJSON(w, rpcOut(req.ID, map[string]any{"prompts": []Prompt{{
				Name: "triage", Description: "Triage an issue",
				Arguments: []PromptArgument{{Name: "issue", Required: true}},
			}}}))
		case "prompts/get":
			writeJSON(w, rpcOut(req.ID, map[string]any{"messages": []PromptMessage{{
				Role: "user", Content: PromptContent{Type: "text", Text: "triage: crash on boot"},
			}}}))
		default:
			writeJSON(w, rpcErrOut(req.ID, -32601, "method not found "+req.Method))
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c, err := NewHTTP(ts.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Resources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].URI != "docs://policy" {
		t.Fatalf("resources = %+v", res)
	}

	contents, err := c.ReadResource(context.Background(), "docs://policy")
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || !strings.Contains(contents[0].Text, "Acme") {
		t.Fatalf("contents = %+v", contents)
	}

	prompts, err := c.Prompts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].Name != "triage" {
		t.Fatalf("prompts = %+v", prompts)
	}

	pr, err := c.GetPrompt(context.Background(), "triage", map[string]string{"issue": "crash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Messages) != 1 || pr.Messages[0].Role != "user" {
		t.Fatalf("prompt result = %+v", pr)
	}
}

// ---------------------------------------------------------------------------
// server streamable-HTTP transport (2025-06-18)
// ---------------------------------------------------------------------------

func rpcPost(t *testing.T, url string, accept string, payload string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

func TestMCPHTTPServerTransport(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{Content: "pong", FinishReason: "stop"},
		{Content: "pong", FinishReason: "stop"},
		{Content: "pong", FinishReason: "stop"},
	}})
	a := agent.New("echo", p)
	srv := NewServer([]*agent.Agent{a}, ServerOptions{Name: "ernest-test", Version: "1.2.3"})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// initialize (JSON response)
	resp, body := rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("initialize: status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, `"name":"ernest-test"`) || !strings.Contains(body, `"version":"1.2.3"`) {
		t.Fatalf("initialize body = %s", body)
	}

	// initialize over SSE accept -> text/event-stream with a message event
	resp, body = rpcPost(t, ts.URL, "text/event-stream", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse init: status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: message") || !strings.Contains(body, "data: ") {
		t.Fatalf("sse body = %q", body)
	}

	// notification -> 202, no body
	resp, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted || body != "" {
		t.Fatalf("notification: status=%d body=%q", resp.StatusCode, body)
	}

	// tools/list -> one agent tool
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if !strings.Contains(body, `"name":"echo"`) {
		t.Fatalf("tools/list body = %s", body)
	}

	// prompts/list + prompts/get
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":3,"method":"prompts/list"}`)
	if !strings.Contains(body, `"name":"chat"`) {
		t.Fatalf("prompts/list body = %s", body)
	}
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"chat","arguments":{"input":"hello"}}}`)
	if !strings.Contains(body, `"role":"user"`) || !strings.Contains(body, "hello") {
		t.Fatalf("prompts/get body = %s", body)
	}

	// resources/list -> empty; resources/read -> JSON-RPC error
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`)
	if !strings.Contains(body, `"resources":[]`) {
		t.Fatalf("resources/list body = %s", body)
	}
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"docs://x"}}`)
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("resources/read body = %s", body)
	}

	// tools/call runs the agent
	_, body = rpcPost(t, ts.URL, "application/json", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"input":"hi"}}}`)
	if !strings.Contains(body, "pong") {
		t.Fatalf("tools/call body = %s", body)
	}

	// GET / -> SSE stream kept open
	getResp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("GET content-type = %q", getResp.Header.Get("Content-Type"))
	}
	getResp.Body.Close()

	// OPTIONS -> CORS preflight
	optReq, _ := http.NewRequest(http.MethodOptions, ts.URL, nil)
	optResp, err := http.DefaultClient.Do(optReq)
	if err != nil {
		t.Fatal(err)
	}
	optResp.Body.Close()
	if optResp.StatusCode != http.StatusNoContent || optResp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("options: status=%d cors=%q", optResp.StatusCode, optResp.Header.Get("Access-Control-Allow-Origin"))
	}

	// Go client round trip against the real HTTP server
	c, err := NewHTTP(ts.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("client tools = %+v", tools)
	}
	out, err := c.Call(context.Background(), "echo", map[string]any{"input": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text(), "pong") {
		t.Fatalf("client call = %+v", out)
	}
	pr, err := c.GetPrompt(context.Background(), "chat", map[string]string{"input": "hello", "agent": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Messages) != 1 || !strings.Contains(pr.Messages[0].Content.Text, "hello") {
		t.Fatalf("client prompt = %+v", pr)
	}
	resList, err := c.Resources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(resList) != 0 {
		t.Fatalf("client resources = %+v", resList)
	}
}

func TestStdioFullFlow(t *testing.T) {
	c := newStdioTestClient(t)
	defer c.Close()
	ctx := context.Background()

	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "fake_echo" || tools[1].Name != "fake_fail" {
		t.Fatalf("tools = %+v", tools)
	}
	res, err := c.Call(ctx, "fake_echo", map[string]any{"message": "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "echo: hi there" || res.IsError {
		t.Fatalf("result = %+v", res)
	}
}

func TestStdioToolError(t *testing.T) {
	c := newStdioTestClient(t)
	defer c.Close()
	// MCP-level call succeeds; the tool itself reports isError.
	res, err := c.Call(context.Background(), "fake_fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Text() != "boom" {
		t.Fatalf("result = %+v", res)
	}
}

func TestStdioRPCMethodError(t *testing.T) {
	c := newStdioTestClient(t)
	defer c.Close()
	_, err := c.Call(context.Background(), "ghost", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %v", err)
	}
}

func TestStdioCloseKillsProcess(t *testing.T) {
	c := newStdioTestClient(t)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// After close, requests fail fast (process is gone).
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("request after close must fail")
	}
}

func TestStdioMissingCommand(t *testing.T) {
	if _, err := NewStdio("definitely-not-a-real-command-xyz", nil, Options{}); err == nil {
		t.Fatal("missing command must error")
	}
	if _, err := NewStdio("", nil, Options{}); err == nil {
		t.Fatal("empty command must error")
	}
}

// ---------------------------------------------------------------------------
// core.Tool mapping
// ---------------------------------------------------------------------------

func TestAsCoreTools(t *testing.T) {
	f := newFakeHTTPServer(t, "json")
	defer f.srv.Close()
	c, err := NewHTTP(f.srv.URL+"/mcp", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	tools, err := c.AsCoreTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "fake_echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if !strings.Contains(string(tools[0].Parameters), "message") {
		t.Fatalf("schema not carried over: %s", tools[0].Parameters)
	}

	// Running the core tool forwards to the server.
	out, err := tools[0].Run(ctx, core.NewToolContext("t", "r"), json.RawMessage(`{"message":"hey"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(*Result)
	if !ok || res.Text() != "echo: hey" {
		t.Fatalf("run result = %+v", out)
	}

	// Invalid arguments are rejected locally.
	if _, err := tools[0].Run(ctx, core.NewToolContext("t", "r"), json.RawMessage(`not json`)); err == nil {
		t.Fatal("invalid args must error")
	}
}

func TestAsCoreToolsIsError(t *testing.T) {
	c := newStdioTestClient(t)
	defer c.Close()
	tools, err := c.AsCoreTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var failTool *core.Tool
	for _, tl := range tools {
		if tl.Name == "fake_fail" {
			failTool = tl
		}
	}
	if failTool == nil {
		t.Fatalf("fake_fail missing: %+v", tools)
	}
	_, err = failTool.Run(context.Background(), core.NewToolContext("t", "r"), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
