package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
)

// runPackTool invokes a pack tool with raw JSON args, returning the error
// message ("" on success). No browser is launched for these paths.
func runPackTool(t *testing.T, tool *core.Tool, args string) (string, bool) {
	t.Helper()
	_, err := tool.Run(context.Background(), nil, json.RawMessage(args))
	if err != nil {
		return err.Error(), false
	}
	return "", true
}

func TestPackToolNamesRegistered(t *testing.T) {
	byName := core.ToolsByName(Tools)
	for _, name := range []string{NavigateName, ReadName, ClickName, TypeName, ScreenshotName} {
		if byName[name] == nil {
			t.Fatalf("browser pack tool %s missing", name)
		}
	}
	if len(Tools) != 5 {
		t.Fatalf("pack size = %d", len(Tools))
	}
}

func TestPackToolsValidateBeforeLaunch(t *testing.T) {
	// Malformed calls must fail during validation, before any browser
	// launch (launching requires a real Edge/Chrome binary and would
	// fail differently — with "no Edge or Chrome binary found").
	cases := []struct {
		tool *core.Tool
		args string
		want string
	}{
		{Navigate, `{}`, "url is required"},
		{Click, `{}`, "selector is required"},
		{Type, `{"text":"hi"}`, "selector is required"},
	}
	for _, c := range cases {
		msg, ok := runPackTool(t, c.tool, c.args)
		if ok {
			t.Fatalf("%s(%s) must fail", c.tool.Name, c.args)
		}
		if !strings.Contains(msg, c.want) {
			t.Fatalf("%s(%s) msg = %q, want %q", c.tool.Name, c.args, msg, c.want)
		}
		if strings.Contains(msg, "no Edge or Chrome binary found") {
			t.Fatalf("%s launched the browser before validating: %q", c.tool.Name, msg)
		}
	}
}

// TestPackToolsFailBeforeLaunch is the launch-order guarantee: a valid
// call that cannot find a browser binary reports discovery failure, never
// a validation error.
func TestPackToolsFailBeforeLaunch(t *testing.T) {
	msg, ok := runPackTool(t, Navigate, `{"url":"https://example.com"}`)
	if ok {
		t.Skip("a browser binary exists on this machine; discovery error untestable")
	}
	if !strings.Contains(msg, "no Edge or Chrome binary found") {
		t.Fatalf("msg = %q", msg)
	}
}
