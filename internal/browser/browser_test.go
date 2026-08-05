package browser

import (
	"context"
	"os"
	"strings"
	"testing"
)

// runTool invokes the browser tool with raw JSON args, returning the
// error message ("" on success). No browser is launched for these paths.
func runTool(t *testing.T, args string) (string, bool) {
	t.Helper()
	res, err := Tool.Run(context.Background(), nil, []byte(args))
	if err != nil {
		return err.Error(), false
	}
	_ = res
	return "", true
}

func TestBrowserToolUnknownAction(t *testing.T) {
	msg, ok := runTool(t, `{"action":"teleport","url":"https://example.com"}`)
	if ok {
		t.Fatal("expected error")
	}
	if !strings.Contains(msg, "unknown action") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestBrowserToolMissingAction(t *testing.T) {
	msg, ok := runTool(t, `{}`)
	if ok {
		t.Fatal("expected error")
	}
	if !strings.Contains(msg, "action is required") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestBrowserToolMissingURL(t *testing.T) {
	// open without url fails during validation, before any launch.
	msg, ok := runTool(t, `{"action":"open"}`)
	if ok {
		t.Fatal("expected error")
	}
	if !strings.Contains(msg, "url is required") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestBrowserToolWaitNeedsNoBrowser(t *testing.T) {
	// wait short-circuits in validation: no browser is launched.
	msg, ok := runTool(t, `{"action":"wait","ms":1}`)
	if !ok || msg != "" {
		t.Fatalf("wait failed: %q ok=%v", msg, ok)
	}
}

// TestBrowserOpenReal exercises a real headless Edge/Chrome. It only runs
// when ERNEST_BROWSER_TEST=1 is set and a browser binary exists.
func TestBrowserOpenReal(t *testing.T) {
	if os.Getenv("ERNEST_BROWSER_TEST") != "1" {
		t.Skip("set ERNEST_BROWSER_TEST=1 to run real-browser tests")
	}
	bin, err := findBrowser()
	if err != nil {
		t.Skip(err)
	}
	inst, err := launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer inst.Close()

	ctx := context.Background()
	res, err := inst.html(ctx, "https://example.com")
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	if !strings.Contains(res.(string), "<html") {
		t.Fatalf("html missing <html: %.120s", res)
	}
}
