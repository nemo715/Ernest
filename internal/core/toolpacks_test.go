package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// packCtx builds a ToolContext with a fresh sandbox directory.
func packCtx(t *testing.T) (*ToolContext, string) {
	t.Helper()
	dir := t.TempDir()
	return &ToolContext{AgentName: "t", RunID: "r", Sandbox: dir, HTTP: &http.Client{}}, dir
}

// packRun runs a tool with raw JSON args and returns the result as a
// generic map (nil map on error).
func packRun(t *testing.T, tool *Tool, tc *ToolContext, args string) (map[string]any, error) {
	t.Helper()
	out, err := tool.Run(context.Background(), tc, json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	return m, nil
}

func mustRun(t *testing.T, tool *Tool, tc *ToolContext, args string) map[string]any {
	t.Helper()
	m, err := packRun(t, tool, tc, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", tool.Name, args, err)
	}
	return m
}

func TestFileToolsRoundTrip(t *testing.T) {
	tc, dir := packCtx(t)

	// write
	wm := mustRun(t, FileWrite, tc, `{"path":"notes.txt","content":"hello sandbox"}`)
	if wm["bytes"] != float64(13) {
		t.Fatalf("write bytes = %v", wm["bytes"])
	}
	// read back
	rm := mustRun(t, FileRead, tc, `{"path":"notes.txt"}`)
	if rm["content"] != "hello sandbox" {
		t.Fatalf("read content = %q", rm["content"])
	}
	if rm["truncated"] != false {
		t.Fatalf("read truncated = %v", rm["truncated"])
	}
	// append
	mustRun(t, FileWrite, tc, `{"path":"notes.txt","content":"\nmore","append":true}`)
	rm2 := mustRun(t, FileRead, tc, `{"path":"notes.txt"}`)
	if rm2["content"] != "hello sandbox\nmore" {
		t.Fatalf("appended content = %q", rm2["content"])
	}
	// list
	lm := mustRun(t, FileList, tc, `{}`)
	entries, ok := lm["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %+v", lm["entries"])
	}
	first := entries[0].(map[string]any)
	if first["name"] != "notes.txt" || first["dir"] != false {
		t.Fatalf("first entry = %+v", first)
	}
	// list a subdirectory path
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	lm2 := mustRun(t, FileList, tc, `{"path":"sub"}`)
	if entries, ok := lm2["entries"].([]any); !ok || len(entries) != 0 {
		t.Fatalf("sub entries = %+v", lm2["entries"])
	}
}

func TestFileReadCapAndMissing(t *testing.T) {
	tc, dir := packCtx(t)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("a", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	rm := mustRun(t, FileRead, tc, `{"path":"big.txt","maxBytes":10}`)
	if rm["content"] != "aaaaaaaaaa" {
		t.Fatalf("capped content = %q", rm["content"])
	}
	if rm["truncated"] != true {
		t.Fatalf("truncated = %v", rm["truncated"])
	}
	_, err := packRun(t, FileRead, tc, `{"path":"ghost.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("missing file err = %v", err)
	}
}

func TestFileToolsRequireSandbox(t *testing.T) {
	// Approval is injected so shell_exec proceeds past its approval gate
	// and hits the sandbox requirement.
	tc := &ToolContext{AgentName: "t", RunID: "r", Approval: map[string]bool{"ap": true}}
	for _, tool := range []*Tool{FileRead, FileWrite, FileList, ShellExec} {
		var args string
		switch tool.Name {
		case ToolShellExec:
			args = `{"command":"echo hi"}`
		default:
			args = `{"path":"x"}`
		}
		_, err := tool.Run(context.Background(), tc, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "toolSandbox") {
			t.Fatalf("%s without sandbox err = %v", tool.Name, err)
		}
	}
}

func TestFileToolsSandboxEscapeRejected(t *testing.T) {
	tc, dir := packCtx(t)
	outside := filepath.Join(filepath.Dir(dir), "escaped.txt")
	defer os.Remove(outside)
	cases := []struct {
		tool *Tool
		args string
		want string
	}{
		{FileRead, `{"path":"../escaped.txt"}`, "escapes"},
		{FileRead, `{"path":"..\\escaped.txt"}`, "escapes"},
		{FileWrite, `{"path":"../escaped.txt","content":"nope"}`, "escapes"},
		{FileWrite, `{"path":"C:/Windows/win.ini","content":"nope"}`, "absolute"},
		{FileList, `{"path":".."}`, "escapes"},
	}
	for _, c := range cases {
		_, err := packRun(t, c.tool, tc, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s(%s) err = %v, want %q", c.tool.Name, c.args, err, c.want)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("escape wrote outside the sandbox: %v", err)
	}
}

func TestWebSearchParsesDuckDuckGo(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&amp;rut=abc">The Go Programming Language</a>
  <a class="result__snippet" href="https://go.dev/doc/">Go is an open source programming language...</a>
</div>
<div class="result results_links">
  <a class="result__a" href="https://example.com/plain">Plain Link</a>
  <a class="result__snippet" href="https://example.com/plain">Example <b>snippet</b> &amp; more.</a>
</div>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "golang" {
			t.Errorf("q = %q, want golang", got)
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	}))
	defer srv.Close()
	t.Setenv("ERNEST_WEB_SEARCH_URL", srv.URL)

	tc := &ToolContext{AgentName: "t", RunID: "r", HTTP: srv.Client()}
	m := mustRun(t, WebSearch, tc, `{"query":"golang"}`)
	results, ok := m["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %+v", m["results"])
	}
	first := results[0].(map[string]any)
	if first["title"] != "The Go Programming Language" {
		t.Fatalf("title = %q", first["title"])
	}
	if first["url"] != "https://go.dev/doc/" {
		t.Fatalf("redirect url = %q", first["url"])
	}
	if first["snippet"] != "Go is an open source programming language..." {
		t.Fatalf("snippet = %q", first["snippet"])
	}
	second := results[1].(map[string]any)
	if second["url"] != "https://example.com/plain" {
		t.Fatalf("plain url = %q", second["url"])
	}
	if second["snippet"] != "Example snippet & more." {
		t.Fatalf("stripped snippet = %q", second["snippet"])
	}
}

func TestWebSearchErrors(t *testing.T) {
	tc := &ToolContext{AgentName: "t", RunID: "r", HTTP: &http.Client{}}
	// empty query
	_, err := packRun(t, WebSearch, tc, `{"query":""}`)
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("empty query err = %v", err)
	}
	// non-200 endpoint
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("ERNEST_WEB_SEARCH_URL", srv.URL)
	_, err = packRun(t, WebSearch, tc, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("500 err = %v", err)
	}
	// endpoint without parseable results
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>layout changed</body></html>")
	}))
	defer srv2.Close()
	t.Setenv("ERNEST_WEB_SEARCH_URL", srv2.URL)
	_, err = packRun(t, WebSearch, tc, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "no results parsed") {
		t.Fatalf("no-results err = %v", err)
	}
}

func TestShellExecRefusesWithoutApproval(t *testing.T) {
	tc, _ := packCtx(t)
	_, err := ShellExec.Run(context.Background(), tc, json.RawMessage(`{"command":"echo hi"}`))
	var are *ApprovalRequiredError
	if !errors.As(err, &are) {
		t.Fatalf("err = %v, want ApprovalRequiredError", err)
	}
	if are.Action != ToolShellExec {
		t.Fatalf("action = %q", are.Action)
	}
	if !strings.Contains(are.Summary, "echo hi") {
		t.Fatalf("summary = %q (command must be audit-visible)", are.Summary)
	}
	if are.Context == nil || are.Context["command"] != "echo hi" {
		t.Fatalf("context = %+v (full command must be audit-logged)", are.Context)
	}
}

func TestShellExecRunsWithApproval(t *testing.T) {
	tc, dir := packCtx(t)
	tc.Approval = map[string]bool{"ap": true}
	m := mustRun(t, ShellExec, tc, `{"command":"echo hello-from-shell"}`)
	if m["exitCode"] != float64(0) {
		t.Fatalf("exitCode = %v", m["exitCode"])
	}
	out, _ := m["output"].(string)
	if !strings.Contains(out, "hello-from-shell") {
		t.Fatalf("output = %q", out)
	}
	if m["sandbox"] != dir {
		t.Fatalf("sandbox = %q, want %q", m["sandbox"], dir)
	}
	// Rejection via the injected decision is reported as an interrupt.
	tc.Approval = map[string]bool{"ap": false}
	_, err := ShellExec.Run(context.Background(), tc, json.RawMessage(`{"command":"echo nope"}`))
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("rejected err = %v", err)
	}
}

func TestShellExecTimeout(t *testing.T) {
	tc, _ := packCtx(t)
	tc.Approval = map[string]bool{"ap": true}
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		command = "ping -n 6 127.0.0.1 >nul"
	}
	args := fmt.Sprintf(`{"command":%q,"timeoutSeconds":1}`, command)
	_, err := packRun(t, ShellExec, tc, args)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestToolPacksRegistered(t *testing.T) {
	idx := ToolsByName(BuiltinTools)
	for _, name := range []string{ToolFileRead, ToolFileWrite, ToolFileList, ToolWebSearch, ToolShellExec} {
		if idx[name] == nil {
			t.Fatalf("builtin tool %s missing", name)
		}
	}
}
