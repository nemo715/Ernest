// Command claw is a local AI worker built on ernest, OpenClaw-style:
// one agent with hands — filesystem, shell, browser — where every
// shell command and file write pauses for human approval.
//
//	$ go run .            # mock mode (scripted, offline)
//	$ OPENROUTER_API_KEY=sk-... go run .   # real mode
//
// UI: http://127.0.0.1:8081
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
	"github.com/nemo715/Ernest/server"
	"github.com/nemo715/Ernest/storage"
)

const (
	addr     = "127.0.0.1:8081"
	baseURL  = "https://openrouter.ai/api/v1"
	model    = "openai/gpt-4o-mini"
	apiKeyID = "OPENROUTER_API_KEY"

	maxReadBytes  = 128 * 1024
	maxShellBytes = 64 * 1024
)

// ---------------------------------------------------------------------------
// Custom tools: the worker's hands.
// ---------------------------------------------------------------------------

type listDirArgs struct {
	Path string `json:"path" jsonschema:"Directory to list (default: current directory)"`
}

type dirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

var listDirTool = core.MustTool[listDirArgs]("list_dir", "List the entries of a directory", func(ctx context.Context, _ *core.ToolContext, args listDirArgs) (any, error) {
	path := args.Path
	if path == "" {
		path = "."
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		size := int64(0)
		if err == nil {
			size = info.Size()
		}
		out = append(out, dirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	return map[string]any{"path": path, "count": len(out), "entries": out}, nil
})

type readFileArgs struct {
	Path string `json:"path" jsonschema:"File to read"`
}

var readFileTool = core.MustTool[readFileArgs]("read_file", "Read a text file (truncated at 128 KB)", func(ctx context.Context, _ *core.ToolContext, args readFileArgs) (any, error) {
	info, err := os.Stat(args.Path)
	if err != nil {
		return nil, err
	}
	limit := int64(maxReadBytes)
	truncated := false
	if info.Size() > limit {
		truncated = true
	}
	f, err := os.Open(args.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, limit)
	n, err := f.Read(data)
	if err != nil && err.Error() != "EOF" {
		return nil, err
	}
	return map[string]any{"path": args.Path, "bytes": n, "truncated": truncated, "content": string(data[:n])}, nil
})

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"File to create or overwrite"`
	Content string `json:"content" jsonschema:"Text content to write"`
}

var writeFileTool = core.MustTool[writeFileArgs]("write_file", "Create or overwrite a text file (requires approval)", func(ctx context.Context, _ *core.ToolContext, args writeFileArgs) (any, error) {
	if args.Path == "" {
		return nil, errors.New("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(args.Path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": args.Path, "bytes": len(args.Content)}, nil
})

type runShellArgs struct {
	Command    string `json:"command" jsonschema:"Shell command to execute (cmd /c on Windows)"`
	TimeoutSec int    `json:"timeoutSec,omitempty" jsonschema:"Timeout in seconds (default 30)"`
}

type shellResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
}

var runShellTool = core.MustTool[runShellArgs]("run_shell", "Run a shell command and capture output (requires approval)", func(ctx context.Context, _ *core.ToolContext, args runShellArgs) (any, error) {
	if args.Command == "" {
		return nil, errors.New("command is required")
	}
	timeout := time.Duration(args.TimeoutSec) * time.Second
	if args.TimeoutSec <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd", "/c", args.Command)
	cmd.Env = os.Environ()
	stdout, err := cmd.Output()
	stderr := ""
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimRight(string(ee.Stderr), "\r\n")
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return map[string]any{"command": args.Command, "exitCode": -1, "stdout": "", "stderr": "timed out after " + timeout.String(), "timedOut": true}, nil
		}
	}
	out := string(stdout)
	if len(out) > maxShellBytes {
		out = out[:maxShellBytes] + "\n...[truncated]"
	}
	if len(stderr) > maxShellBytes {
		stderr = stderr[:maxShellBytes] + "\n...[truncated]"
	}
	exitCode := 0
	if err != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return shellResult{Command: args.Command, ExitCode: exitCode, Stdout: out, Stderr: stderr}, nil
})

// ---------------------------------------------------------------------------
// Providers: real model when a key is present, scripted mock otherwise.
// ---------------------------------------------------------------------------

func workerInstructions() string {
	return `You are CLAW, a local AI worker running on the user's machine.

You have real hands and must use them instead of guessing:
- list_dir / read_file — inspect the filesystem (no approval needed)
- run_shell — execute shell commands (cmd /c on Windows). ALWAYS asks the user for approval first; the run pauses until they decide. After approval you receive the real output. Never fabricate command output.
- write_file — create/overwrite files. Also approval-gated.
- browser — open pages, read HTML, take screenshots, evaluate JS on a real Edge/Chrome instance.
- http_fetch — fetch a URL over HTTP(S).
- calculator / now — exact math and current time.

Before each tool call, say in one short sentence what you are about to do. After a tool returns, summarize the result for the user concisely. If a command fails, report the error honestly and propose the next step. Never invent file contents, command output, or web page content.`
}

func realProviders(key string) map[string]llm.Provider {
	cfg := llm.OpenAICompatConfig{BaseURL: baseURL, APIKey: key, Model: model}
	return map[string]llm.Provider{"worker": llm.NewOpenAICompat(cfg)}
}

// mockProviders runs fully offline with a scripted story: the worker asks to
// run a shell command, the run pauses for approval, then completes.
func mockProviders() map[string]llm.Provider {
	return map[string]llm.Provider{"worker": llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{
			ID:   "call_1",
			Name: "run_shell",
			Arguments: json.RawMessage(`{"command":"echo hello from claw && echo probe > claw-probe.txt && dir /b claw-probe.txt"}`),
		}}, FinishReason: "tool_calls"},
		{Content: "Done! The command ran after your approval:\n\n```\nhello from claw\nclaw-probe.txt\n```\n\nIt also created `claw-probe.txt` in the working directory as requested.", FinishReason: "stop"},
	}})}
}

// ---------------------------------------------------------------------------
// App: server + small info endpoint.
// ---------------------------------------------------------------------------

type app struct {
	mode  string
	tools []string
	cwd   string
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"mode":             a.mode,
		"model":            model,
		"agent":            "worker",
		"tools":            a.tools,
		"approvalRequired": []string{"run_shell", "write_file"},
		"cwd":              a.cwd,
	})
}

func main() {
	mode := "mock"
	providers := mockProviders()
	if key := os.Getenv(apiKeyID); key != "" {
		mode = "real"
		providers = realProviders(key)
	}

	worker := agent.New("worker", providers["worker"])
	worker.Instructions = workerInstructions()
	worker.Tools = append([]*core.Tool{}, core.BuiltinTools...)
	worker.Tools = append(worker.Tools, core.BrowserTool, listDirTool, readFileTool, writeFileTool, runShellTool)
	worker.RequireApprovalTools = []string{"run_shell", "write_file"}
	// HITL resume replays the stored session: the worker needs its own
	// store even though the server also gets one for /api/sessions.
	worker.Store = storage.NewInMemoryStore()

	toolNames := make([]string, 0, len(worker.Tools))
	for _, t := range worker.Tools {
		toolNames = append(toolNames, t.Name)
	}

	srv, err := server.New(server.Options{
		Agents: []*agent.Agent{worker},
		Store:  storage.NewInMemoryStore(),
		Static: "ui",
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	wd, _ := os.Getwd()
	a := &app{mode: mode, tools: toolNames, cwd: wd}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/info", a.handleInfo)
	mux.Handle("/", srv.Handler())

	log.Printf("claw (%s mode) on http://%s  —  agent: %s, tools: %v", mode, addr, "worker", a.tools)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
