package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/storage"
)

// packAgentConfig returns a mock agent config with the full file pack,
// shell_exec enabled, and a sandbox.
func packAgentConfig() *Config {
	return &Config{
		Agents: []AgentConfig{{
			Name:        "worker",
			Provider:    "mock",
			Model:       "mock-1",
			Tools:       []string{"file_read", "file_write", "file_list", "shell_exec"},
			ToolSandbox: "sandbox",
			ToolPolicy:  &ToolPolicyConfig{EnableShell: true},
		}},
		Store: StoreConfig{Type: "memory"},
	}
}

func TestValidateToolPolicyOK(t *testing.T) {
	c := packAgentConfig()
	c.Agents[0].ToolPolicy = &ToolPolicyConfig{
		EnableShell:     true,
		AutoApprove:     []string{"file_write"},
		RequireApproval: []string{"file_read"},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateToolPolicyErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"file tool without sandbox", func(c *Config) {
			c.Agents[0].ToolSandbox = ""
		}, "toolSandbox"},
		{"shell without enableShell", func(c *Config) {
			c.Agents[0].ToolPolicy.EnableShell = false
		}, "disabled by default"},
		{"shell attached without policy", func(c *Config) {
			c.Agents[0].ToolPolicy = nil
		}, "disabled by default"},
		{"autoApprove shell", func(c *Config) {
			c.Agents[0].ToolPolicy.AutoApprove = []string{"shell_exec"}
		}, "never appear in toolPolicy.autoApprove"},
		{"unknown autoApprove tool", func(c *Config) {
			c.Agents[0].ToolPolicy.AutoApprove = []string{"teleporter"}
		}, "unknown tool"},
		{"unknown requireApproval tool", func(c *Config) {
			c.Agents[0].ToolPolicy.RequireApproval = []string{"ghost"}
		}, "unknown tool"},
		{"requireApproval not attached", func(c *Config) {
			c.Agents[0].ToolPolicy.RequireApproval = []string{"http_fetch"}
		}, "not attached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := packAgentConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApprovalToolDefaults(t *testing.T) {
	ac := AgentConfig{Tools: []string{"file_read", "file_write", "file_list", "web_search", "browser_navigate", "browser_click", "browser_screenshot"}}
	got := approvalTools(ac)
	want := map[string]bool{"file_write": true, "browser_navigate": true, "browser_click": true, "browser_screenshot": true}
	if len(got) != len(want) {
		t.Fatalf("approval tools = %v", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected approval gate %q (all of %v)", name, got)
		}
	}
}

func TestApprovalToolsAutoApproveExempts(t *testing.T) {
	ac := AgentConfig{
		Tools: []string{"file_read", "file_write", "file_list", "shell_exec", "browser_navigate", "browser_click"},
		ToolPolicy: &ToolPolicyConfig{
			EnableShell:     true,
			AutoApprove:     []string{"file_write", "browser_navigate"},
			RequireApproval: []string{"file_read"},
		},
	}
	got := approvalTools(ac)
	want := map[string]bool{"shell_exec": true, "browser_click": true, "file_read": true}
	if len(got) != len(want) {
		t.Fatalf("approval tools = %v", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected approval gate %q (all of %v)", name, got)
		}
	}
}

func TestApprovalToolsShellNeverAutoApproved(t *testing.T) {
	// Even a policy that tries to auto-approve shell_exec (rejected at
	// validation) must not exempt it here — defense in depth.
	ac := AgentConfig{
		Tools:       []string{"shell_exec"},
		ToolSandbox: "s",
		ToolPolicy:  &ToolPolicyConfig{EnableShell: true, AutoApprove: []string{"shell_exec"}},
	}
	got := approvalTools(ac)
	found := false
	for _, name := range got {
		if name == core.ToolShellExec {
			found = true
		}
	}
	if !found {
		t.Fatalf("shell_exec must always require approval, got %v", got)
	}
}

func TestBuildAgentWiresToolPacks(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ernest.json")
	cfg := `{
  "agents": [{
    "name": "worker",
    "provider": "mock",
    "model": "mock-1",
    "tools": ["file_read", "file_write", "file_list", "browser_navigate", "browser_click", "browser_screenshot"],
    "toolSandbox": "sandbox",
    "toolPolicy": { "autoApprove": ["file_write", "browser_navigate"] }
  }]
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := loaded.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if len(rt.Agents) != 1 {
		t.Fatalf("agents = %d", len(rt.Agents))
	}
	ag := rt.Agents[0]
	// Sandbox resolved against the config directory, not the CWD.
	if ag.ToolSandbox != filepath.Join(dir, "sandbox") {
		t.Fatalf("sandbox = %q", ag.ToolSandbox)
	}
	// Attached tools include the file pack + browser pack.
	names := map[string]bool{}
	for _, tool := range ag.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"file_read", "file_write", "file_list", "browser_navigate", "browser_click", "browser_screenshot"} {
		if !names[want] {
			t.Fatalf("tool %s not attached (%v)", want, names)
		}
	}
	// Approval defaults: file_write and browser_navigate auto-approved;
	// browser_click and browser_screenshot remain gated.
	gated := map[string]bool{}
	for _, name := range ag.RequireApprovalTools {
		gated[name] = true
	}
	if gated["file_write"] || gated["browser_navigate"] {
		t.Fatalf("auto-approved tools still gated: %v", ag.RequireApprovalTools)
	}
	if !gated["browser_click"] || !gated["browser_screenshot"] {
		t.Fatalf("default gates missing: %v", ag.RequireApprovalTools)
	}
}

// TestFileWriteEndToEndApprovalFlow drives the full run loop with a
// scripted mock provider: the model calls file_write, the policy pauses
// for HITL approval, nothing is written until the human approves, and the
// approved resume writes the file inside the sandbox.
func TestFileWriteEndToEndApprovalFlow(t *testing.T) {
	dir := t.TempDir()
	call := core.ToolCall{ID: "f1", Name: core.ToolFileWrite, Arguments: []byte(`{"path":"out.txt","content":"hello from tool"}`)}
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "wrote the file", FinishReason: "stop"},
	}})
	a := agent.New("worker", p)
	a.Store = storage.NewInMemoryStore()
	a.Tools = []*core.Tool{core.FileWrite, core.FileRead}
	a.ToolSandbox = dir
	// The same policy buildAgent would produce: file_write gated,
	// file_read free.
	a.RequireApprovalTools = approvalTools(AgentConfig{Tools: []string{"file_write", "file_read"}})

	ctx := context.Background()
	res, err := a.Chat(ctx, "write out.txt", agent.RunOptions{SessionID: "sess-file"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s, want awaiting_approval", res.Status)
	}
	if len(res.Approvals) != 1 || res.Approvals[0].Action != "Run tool file_write" {
		t.Fatalf("approvals = %+v", res.Approvals)
	}
	// The audit trail carries the full arguments.
	if !strings.Contains(res.Approvals[0].Summary, "out.txt") {
		t.Fatalf("approval summary = %q (arguments must be audit-visible)", res.Approvals[0].Summary)
	}
	// Nothing written before approval.
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("file written before approval: %v", err)
	}

	// Approve -> the tool runs exactly once, the file lands in the sandbox.
	res2, err := a.Resume(ctx, core.ApprovalDecision{ApprovalID: res.Approvals[0].ID, Approved: true, Note: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != core.RunStatusCompleted {
		t.Fatalf("resumed status = %s", res2.Status)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("file not written after approval: %v", err)
	}
	if string(raw) != "hello from tool" {
		t.Fatalf("content = %q", raw)
	}
	if res2.Output != "wrote the file" {
		t.Fatalf("output = %q", res2.Output)
	}
}

// TestFileWriteRejectionWritesNothing: a denied approval must not touch
// the sandbox and the model is told about the rejection.
func TestFileWriteRejectionWritesNothing(t *testing.T) {
	dir := t.TempDir()
	call := core.ToolCall{ID: "f1", Name: core.ToolFileWrite, Arguments: []byte(`{"path":"out.txt","content":"nope"}`)}
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "fine, skipped", FinishReason: "stop"},
	}})
	a := agent.New("worker", p)
	a.Store = storage.NewInMemoryStore()
	a.Tools = []*core.Tool{core.FileWrite}
	a.ToolSandbox = dir
	a.RequireApprovalTools = []string{core.ToolFileWrite}

	ctx := context.Background()
	res, err := a.Chat(ctx, "write out.txt", agent.RunOptions{SessionID: "sess-rej"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusAwaitingApproval {
		t.Fatalf("status = %s", res.Status)
	}
	res2, err := a.Resume(ctx, core.ApprovalDecision{ApprovalID: res.Approvals[0].ID, Approved: false})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res2.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected write touched the sandbox: %v", err)
	}
	rejected := false
	for _, m := range res2.Messages {
		if m.Role == core.RoleTool && strings.Contains(m.Content, "rejected") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("rejection feedback missing: %+v", res2.Messages)
	}
}
