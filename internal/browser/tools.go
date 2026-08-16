package browser

// Tool pack — the granular browser tools. Unlike the legacy "browser"
// tool (one tool, one action enum), these are individual tools with
// dedicated argument shapes so models and policy systems can reason
// about each browser capability separately. All five share the lazy
// process-wide browser instance: nothing launches until a validated
// call actually needs it.

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/internal/core"
)

// Browser tool pack name constants (referenced from ernest.json "tools").
const (
	NavigateName   = "browser_navigate"
	ReadName       = "browser_read"
	ClickName      = "browser_click"
	TypeName       = "browser_type"
	ScreenshotName = "browser_screenshot"
)

// Tools is the browser tool pack registry. The pack is intentionally
// NOT part of core.BuiltinTools (it carries the go-rod/CDP dependency);
// config wires it in per agent, exactly like the legacy "browser" tool.
var Tools = []*core.Tool{Navigate, Read, Click, Type, Screenshot}

// NavigateArgs is the argument shape of the browser_navigate tool.
type NavigateArgs struct {
	URL string `json:"url" jsonschema:"The absolute URL to open"`
}

// Navigate opens a URL in the shared browser window and returns the
// resulting page title and final URL (redirects are followed).
var Navigate = core.MustTool[NavigateArgs](NavigateName, "Open a URL in the shared browser window and return the page title and final URL.", func(ctx context.Context, _ *core.ToolContext, args NavigateArgs) (any, error) {
	// Validate before touching the lazy instance: malformed calls must
	// never pay the browser launch cost.
	if args.URL == "" {
		return nil, fmt.Errorf("browser_navigate: url is required")
	}
	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser_navigate: %w", err)
	}
	return b.open(ctx, args.URL)
})

// ReadArgs is the argument shape of the browser_read tool.
type ReadArgs struct {
	URL string `json:"url,omitempty" jsonschema:"Optional URL to navigate to first; omit to read the current page"`
}

// Read returns the current page's HTML (capped at 50 KB). When URL is
// given it navigates first.
var Read = core.MustTool[ReadArgs](ReadName, "Read the shared browser window's current page HTML (capped at 50 KB). Optionally navigate to a URL first.", func(ctx context.Context, _ *core.ToolContext, args ReadArgs) (any, error) {
	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser_read: %w", err)
	}
	return b.html(ctx, args.URL)
})

// ClickArgs is the argument shape of the browser_click tool.
type ClickArgs struct {
	Selector string `json:"selector" jsonschema:"CSS selector of the element to click"`
}

// Click clicks the first element matching a CSS selector.
var Click = core.MustTool[ClickArgs](ClickName, "Click the first element matching a CSS selector in the shared browser window.", func(ctx context.Context, _ *core.ToolContext, args ClickArgs) (any, error) {
	if args.Selector == "" {
		return nil, fmt.Errorf("browser_click: selector is required")
	}
	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser_click: %w", err)
	}
	return b.click(ctx, args.Selector)
})

// TypeArgs is the argument shape of the browser_type tool.
type TypeArgs struct {
	Selector string `json:"selector" jsonschema:"CSS selector of the input element"`
	Text     string `json:"text" jsonschema:"Text to type into the element"`
}

// Type focuses the first element matching a CSS selector and types text
// into it.
var Type = core.MustTool[TypeArgs](TypeName, "Focus the first element matching a CSS selector in the shared browser window and type text into it.", func(ctx context.Context, _ *core.ToolContext, args TypeArgs) (any, error) {
	if args.Selector == "" {
		return nil, fmt.Errorf("browser_type: selector is required")
	}
	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser_type: %w", err)
	}
	return b.typeText(ctx, args.Selector, args.Text)
})

// ScreenshotArgs is the argument shape of the browser_screenshot tool.
type ScreenshotArgs struct {
	URL string `json:"url,omitempty" jsonschema:"Optional URL to navigate to first; omit to capture the current page"`
}

// Screenshot captures the current page as a PNG (base64-encoded).
var Screenshot = core.MustTool[ScreenshotArgs](ScreenshotName, "Capture the shared browser window's current page as a base64-encoded PNG screenshot. Optionally navigate to a URL first.", func(ctx context.Context, _ *core.ToolContext, args ScreenshotArgs) (any, error) {
	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser_screenshot: %w", err)
	}
	return b.screenshot(ctx, args.URL)
})
