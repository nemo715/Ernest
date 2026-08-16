package core

// Tool packs — the safety-conscious built-in tool ecosystem.
//
// Honest guardrails (not optional):
//   - file_read/file_write/file_list run ONLY inside the agent's
//     configured toolSandbox; absolute paths and .. escapes are rejected.
//   - file_write always requires human approval unless the agent's
//     toolPolicy.autoApprove lists it.
//   - shell_exec is disabled by default (toolPolicy.enableShell), ALWAYS
//     requires approval (auto-approve is rejected at validation), and
//     every invocation is audit-logged in the run trace.
//   - web_search uses DuckDuckGo's free HTML endpoint — plain web results
//     only, no news/news-feed/SERP-API coverage.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Tool pack name constants (referenced from ernest.json "tools").
const (
	ToolFileRead  = "file_read"
	ToolFileWrite = "file_write"
	ToolFileList  = "file_list"
	ToolShellExec = "shell_exec"
	ToolWebSearch = "web_search"
)

// ---------------------------------------------------------------------------
// Sandbox helpers
// ---------------------------------------------------------------------------

// sandboxDir resolves the agent's tool sandbox, creating it when missing.
func sandboxDir(tc *ToolContext, toolName string) (string, error) {
	if tc == nil || strings.TrimSpace(tc.Sandbox) == "" {
		return "", NewError(KindValidation, toolName+": toolSandbox is not configured for this agent (set \"toolSandbox\" in ernest.json)")
	}
	abs, err := filepath.Abs(tc.Sandbox)
	if err != nil {
		return "", NewError(KindTool, toolName+": sandbox: "+err.Error(), err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", NewError(KindTool, toolName+": cannot create sandbox: "+err.Error(), err)
	}
	return abs, nil
}

// sandboxPath confines a user-supplied path inside the sandbox directory.
// Absolute paths, volume prefixes and .. escapes are rejected before any
// file operation happens.
func sandboxPath(sandbox, p, toolName string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", NewError(KindValidation, toolName+": path is required")
	}
	if filepath.IsAbs(p) {
		return "", NewError(KindValidation, toolName+": absolute paths are not allowed (sandbox: "+sandbox+")")
	}
	full := filepath.Clean(filepath.Join(sandbox, p))
	rel, err := filepath.Rel(sandbox, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", NewError(KindValidation, toolName+": path escapes the sandbox: "+p)
	}
	return full, nil
}

// ---------------------------------------------------------------------------
// file_read / file_write / file_list
// ---------------------------------------------------------------------------

// FileReadArgs is the argument shape of the file_read tool.
type FileReadArgs struct {
	Path     string `json:"path" jsonschema:"File path relative to the agent's tool sandbox"`
	MaxBytes int    `json:"maxBytes,omitempty" jsonschema:"Cap on returned size (default 32768)"`
}

// FileRead reads a text file inside the agent's tool sandbox.
var FileRead = MustTool[FileReadArgs](ToolFileRead, "Read a text file inside the agent's tool sandbox (configured via \"toolSandbox\" in ernest.json). Paths are relative; escaping the sandbox is rejected.", func(ctx context.Context, tc *ToolContext, args FileReadArgs) (any, error) {
	sandbox, err := sandboxDir(tc, ToolFileRead)
	if err != nil {
		return nil, err
	}
	full, err := sandboxPath(sandbox, args.Path, ToolFileRead)
	if err != nil {
		return nil, err
	}
	maxBytes := args.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 32 << 10
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(KindValidation, ToolFileRead+": file not found: "+args.Path)
		}
		return nil, NewError(KindTool, ToolFileRead+": "+err.Error(), err)
	}
	content := truncateRunes(string(raw), maxBytes)
	return map[string]any{"path": args.Path, "content": content, "bytes": len(raw), "truncated": len(raw) > maxBytes}, nil
})

// FileWriteArgs is the argument shape of the file_write tool.
type FileWriteArgs struct {
	Path    string `json:"path" jsonschema:"File path relative to the agent's tool sandbox"`
	Content string `json:"content" jsonschema:"Text content to write"`
	Append  bool   `json:"append,omitempty" jsonschema:"Append instead of overwriting"`
}

// FileWrite writes a text file inside the agent's tool sandbox.
// file_write always requires human approval unless the agent's
// toolPolicy.autoApprove exempts it (the approval policy is enforced by
// the run loop; this tool only enforces the sandbox).
var FileWrite = MustTool[FileWriteArgs](ToolFileWrite, "Write (or append to) a text file inside the agent's tool sandbox. Always requires human approval unless toolPolicy.autoApprove lists it.", func(ctx context.Context, tc *ToolContext, args FileWriteArgs) (any, error) {
	sandbox, err := sandboxDir(tc, ToolFileWrite)
	if err != nil {
		return nil, err
	}
	full, err := sandboxPath(sandbox, args.Path, ToolFileWrite)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, NewError(KindTool, ToolFileWrite+": "+err.Error(), err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if args.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(full, flags, 0o644)
	if err != nil {
		return nil, NewError(KindTool, ToolFileWrite+": "+err.Error(), err)
	}
	n, err := f.WriteString(args.Content)
	cerr := f.Close()
	if err != nil {
		return nil, NewError(KindTool, ToolFileWrite+": "+err.Error(), err)
	}
	if cerr != nil {
		return nil, NewError(KindTool, ToolFileWrite+": "+cerr.Error(), cerr)
	}
	return map[string]any{"path": args.Path, "bytes": n, "appended": args.Append}, nil
})

// FileListArgs is the argument shape of the file_list tool.
type FileListArgs struct {
	Path string `json:"path,omitempty" jsonschema:"Directory relative to the sandbox (default: sandbox root)"`
}

// FileList lists a directory inside the agent's tool sandbox.
var FileList = MustTool[FileListArgs](ToolFileList, "List files and directories inside the agent's tool sandbox (default: the sandbox root).", func(ctx context.Context, tc *ToolContext, args FileListArgs) (any, error) {
	sandbox, err := sandboxDir(tc, ToolFileList)
	if err != nil {
		return nil, err
	}
	dir := sandbox
	if strings.TrimSpace(args.Path) != "" {
		dir, err = sandboxPath(sandbox, args.Path, ToolFileList)
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, NewError(KindTool, ToolFileList+": "+err.Error(), err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, map[string]any{"name": e.Name(), "dir": e.IsDir(), "size": info.Size()})
	}
	return map[string]any{"path": args.Path, "entries": items}, nil
})

// ---------------------------------------------------------------------------
// web_search
// ---------------------------------------------------------------------------

// webSearchURL is the search endpoint. The ERNEST_WEB_SEARCH_URL override
// exists for tests and self-hosted DuckDuckGo mirrors.
func webSearchURL() string {
	if v := os.Getenv("ERNEST_WEB_SEARCH_URL"); v != "" {
		return v
	}
	return "https://html.duckduckgo.com/html/"
}

// WebSearchArgs is the argument shape of the web_search tool.
type WebSearchArgs struct {
	Query      string `json:"query" jsonschema:"The search query"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"Cap on returned results (default 5, max 10)"`
}

var (
	ddgLinkRE    = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRE = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
)

// WebSearch queries DuckDuckGo's HTML endpoint (no API key) and returns
// title/URL/snippet results. Honest scope: plain web results only — no
// news, news-feed or SERP-API coverage.
var WebSearch = MustTool[WebSearchArgs](ToolWebSearch, "Search the web via DuckDuckGo (no API key needed) and return title/URL/snippet results. Plain web results only — no news/news-feed/SERP coverage.", func(ctx context.Context, tc *ToolContext, args WebSearchArgs) (any, error) {
	if strings.TrimSpace(args.Query) == "" {
		return nil, NewError(KindValidation, ToolWebSearch+": query is required")
	}
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 10 {
		maxResults = 10
	}
	client := tc.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	u := webSearchURL() + "?q=" + url.QueryEscape(args.Query)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, NewError(KindTool, ToolWebSearch+": "+err.Error(), err)
	}
	// The default Go user-agent is frequently blocked; a browser UA is
	// the standard courtesy for this endpoint.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ernest-web-search)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, NewError(KindTool, ToolWebSearch+": "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, NewError(KindTool, fmt.Sprintf("%s: search endpoint returned %d", ToolWebSearch, resp.StatusCode))
	}
	body, err := readCapped(resp, 512<<10)
	if err != nil {
		return nil, NewError(KindTool, ToolWebSearch+": read failed: "+err.Error(), err)
	}
	links := ddgLinkRE.FindAllStringSubmatch(body, maxResults)
	snippets := ddgSnippetRE.FindAllStringSubmatch(body, maxResults)
	results := make([]map[string]string, 0, len(links))
	for i, m := range links {
		if len(m) != 3 {
			continue
		}
		snippet := ""
		if i < len(snippets) && len(snippets[i]) == 2 {
			snippet = stripTags(snippets[i][1])
		}
		results = append(results, map[string]string{
			"title":   stripTags(m[2]),
			"url":     ddgRedirectURL(m[1]),
			"snippet": snippet,
		})
	}
	if len(results) == 0 {
		return nil, NewError(KindTool, ToolWebSearch+": no results parsed (the endpoint layout may have changed)")
	}
	return map[string]any{"query": args.Query, "results": results}, nil
})

// ddgRedirectURL decodes DuckDuckGo's /l/?uddg=<url> redirect wrapper.
func ddgRedirectURL(href string) string {
	if !strings.Contains(href, "uddg=") {
		return href
	}
	if parsed, err := url.Parse(href); err == nil {
		if target := parsed.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return decoded
			}
			return target
		}
	}
	return href
}

func stripTags(s string) string {
	out := htmlAnyTagRE.ReplaceAllString(s, "")
	return strings.TrimSpace(htmlUnescape(out))
}

// htmlUnescape decodes a handful of common entities without the html
// package import cycle risk (toolpacks keep core deps minimal).
func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#x27;", "'", "&nbsp;", " ")
	return r.Replace(s)
}

// readCapped reads up to max bytes from resp.Body.
func readCapped(resp *http.Response, max int64) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ---------------------------------------------------------------------------
// shell_exec
// ---------------------------------------------------------------------------

// ShellExecArgs is the argument shape of the shell_exec tool.
type ShellExecArgs struct {
	Command        string `json:"command" jsonschema:"The shell command to run"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty" jsonschema:"Command timeout in seconds (default 30, max 300)"`
}

// ShellExec runs a shell command inside the agent's tool sandbox.
// Disabled by default (toolPolicy.enableShell) and ALWAYS requires human
// approval — the run loop enforces the policy and this tool refuses to
// run without an injected approval decision as defense in depth. Every
// invocation is audit-logged: the full command lands in the approval
// record and in the run trace's tool span input.
var ShellExec = MustTool[ShellExecArgs](ToolShellExec, "Run a shell command inside the agent's tool sandbox. Disabled by default (toolPolicy.enableShell), always requires human approval, and every command is audit-logged in the run trace.", func(ctx context.Context, tc *ToolContext, args ShellExecArgs) (any, error) {
	if strings.TrimSpace(args.Command) == "" {
		return nil, NewError(KindValidation, ToolShellExec+": command is required")
	}
	if tc == nil {
		return nil, &ApprovalRequiredError{Action: ToolShellExec, Summary: "Run shell command: " + args.Command, Context: map[string]any{"command": args.Command}}
	}
	// Defense in depth: the run loop enforces the requireApproval policy;
	// the tool itself refuses to run without a decision (and honors an
	// injected rejection).
	if err := tc.RequestApproval(ToolShellExec, "Run shell command: "+args.Command, map[string]any{"command": args.Command}); err != nil {
		return nil, err
	}
	sandbox, err := sandboxDir(tc, ToolShellExec)
	if err != nil {
		return nil, err
	}
	timeout := args.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cctx, "cmd", "/C", args.Command)
	} else {
		cmd = exec.CommandContext(cctx, "sh", "-c", args.Command)
	}
	cmd.Dir = sandbox
	out, runErr := cmd.CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return nil, NewError(KindTool, fmt.Sprintf("%s: timed out after %ds", ToolShellExec, timeout))
	}
	result := map[string]any{
		"command":  args.Command,
		"sandbox":  sandbox,
		"exitCode": cmd.ProcessState.ExitCode(),
		"output":   truncateRunes(string(out), 16<<10),
	}
	if runErr != nil {
		result["error"] = runErr.Error()
	}
	return result, nil
})
