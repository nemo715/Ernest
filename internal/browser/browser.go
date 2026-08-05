// Package browser implements the "browser" agent tool: a lazy, shared
// Edge/Chrome instance driven over CDP (via go-rod). The browser is only
// launched on first use, so agents that never touch it pay nothing.
package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/nemo715/Ernest/internal/core"
)

// Tool is the built-in browser tool for agents.
var Tool = core.MustTool[browserArgs](
	"browser",
	"Control a real browser window: open pages, read HTML, take screenshots, click and type. "+
		"Actions: open {url}; html {url?}; screenshot {url?}; eval {js}; click {selector}; type {selector,text}; wait {ms?}; close.",
	browserRun,
)

type browserArgs struct {
	Action   string `json:"action"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	JS       string `json:"js,omitempty"`
	MS       int    `json:"ms,omitempty"`
}

func browserRun(ctx context.Context, _ *core.ToolContext, args browserArgs) (any, error) {
	// Validate arguments before touching the (lazy) browser instance so
	// malformed calls never pay the launch cost.
	switch args.Action {
	case "open":
		if args.URL == "" {
			return nil, fmt.Errorf("browser: url is required")
		}
	case "html", "screenshot":
		// url optional — uses the current page
	case "eval":
		if args.JS == "" {
			return nil, fmt.Errorf("browser: js is required for eval")
		}
	case "click", "type":
		if args.Selector == "" {
			return nil, fmt.Errorf("browser: selector is required for %s", args.Action)
		}
	case "wait":
		ms := args.MS
		if ms <= 0 {
			ms = 500
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
			return "waited", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "":
		return nil, fmt.Errorf("browser: action is required (open|html|screenshot|eval|click|type|wait|close)")
	default:
		return nil, fmt.Errorf("browser: unknown action %q (open|html|screenshot|eval|click|type|wait|close)", args.Action)
	}

	b, err := Shared()
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	switch args.Action {
	case "open":
		return b.open(ctx, args.URL)
	case "html":
		return b.html(ctx, args.URL)
	case "screenshot":
		return b.screenshot(ctx, args.URL)
	case "eval":
		return b.eval(ctx, args.JS)
	case "click":
		return b.click(ctx, args.Selector)
	case "type":
		return b.typeText(ctx, args.Selector, args.Text)
	case "close":
		b.Close()
		return "browser closed", nil
	}
	return nil, fmt.Errorf("browser: unhandled action %q", args.Action)
}

// ---------------------------------------------------------------------------
// instance management
// ---------------------------------------------------------------------------

var (
	sharedMu sync.Mutex
	shared   *Instance
)

// Shared returns the process-wide browser instance, launching it lazily.
func Shared() (*Instance, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		bin, err := findBrowser()
		if err != nil {
			return nil, err
		}
		inst, err := launch(bin)
		if err != nil {
			return nil, err
		}
		shared = inst
	}
	return shared, nil
}

// Instance wraps one browser process and its active page.
type Instance struct {
	mu      sync.Mutex
	browser *rod.Browser
	page    *rod.Page
}

func launch(bin string) (*Instance, error) {
	u, err := launcher.New().
		Bin(bin).
		Headless(true).
		NoSandbox(true).
		Leakless(false). // leakless helper is flagged by some AVs
		Launch()
	if err != nil {
		return nil, err
	}
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		return nil, err
	}
	inst := &Instance{browser: b}
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		b.Close()
		return nil, err
	}
	inst.page = page
	return inst, nil
}

// Close releases the browser process. The shared instance is reset so the
// next use relaunches.
func (i *Instance) Close() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.browser != nil {
		_ = i.browser.Close()
		i.browser = nil
		i.page = nil
	}
	sharedMu.Lock()
	if shared == i {
		shared = nil
	}
	sharedMu.Unlock()
}

// pageCtx applies a per-operation timeout.
func (i *Instance) pageCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}

func (i *Instance) currentPage() *rod.Page {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.page
}

func (i *Instance) navigate(ctx context.Context, url string) error {
	if url == "" {
		return fmt.Errorf("browser: url is required")
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	return i.currentPage().Context(pctx).Navigate(url)
}

func (i *Instance) open(ctx context.Context, url string) (any, error) {
	if err := i.navigate(ctx, url); err != nil {
		return nil, err
	}
	p := i.currentPage()
	title, _ := p.Info()
	info := map[string]any{"url": p.MustInfo().URL}
	if title != nil {
		info["title"] = title.Title
	}
	return info, nil
}

func (i *Instance) html(ctx context.Context, url string) (any, error) {
	if url != "" {
		if err := i.navigate(ctx, url); err != nil {
			return nil, err
		}
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	html, err := i.currentPage().Context(pctx).HTML()
	if err != nil {
		return nil, err
	}
	const max = 50000
	if len(html) > max {
		html = html[:max] + "\n... (truncated)"
	}
	return html, nil
}

func (i *Instance) screenshot(ctx context.Context, url string) (any, error) {
	if url != "" {
		if err := i.navigate(ctx, url); err != nil {
			return nil, err
		}
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	data, err := i.currentPage().Context(pctx).Screenshot(true, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"mimeType": "image/png",
		"base64":   base64.StdEncoding.EncodeToString(data),
		"bytes":    len(data),
	}, nil
}

func (i *Instance) eval(ctx context.Context, js string) (any, error) {
	if js == "" {
		return nil, fmt.Errorf("browser: js is required for eval")
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	res, err := i.currentPage().Context(pctx).Eval(js)
	if err != nil {
		return nil, err
	}
	return res.Value.Val(), nil
}

func (i *Instance) click(ctx context.Context, selector string) (any, error) {
	if selector == "" {
		return nil, fmt.Errorf("browser: selector is required for click")
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	p := i.currentPage().Context(pctx)
	target, err := p.Element(selector)
	if err != nil {
		return nil, err
	}
	if err := target.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, err
	}
	return "clicked " + selector, nil
}

func (i *Instance) typeText(ctx context.Context, selector, text string) (any, error) {
	if selector == "" {
		return nil, fmt.Errorf("browser: selector is required for type")
	}
	pctx, cancel := i.pageCtx(ctx)
	defer cancel()
	p := i.currentPage().Context(pctx)
	el, err := p.Element(selector)
	if err != nil {
		return nil, err
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, err
	}
	if err := el.Input(text); err != nil {
		return nil, err
	}
	return fmt.Sprintf("typed %q into %s", text, selector), nil
}

// ---------------------------------------------------------------------------
// browser discovery
// ---------------------------------------------------------------------------

// findBrowser locates Microsoft Edge or Google Chrome. The first existing
// binary wins.
func findBrowser() (string, error) {
	candidates := browserCandidates()
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	// PATH lookup as a last resort (msedge/chrome on any platform).
	for _, name := range []string{"msedge", "microsoft-edge", "google-chrome", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Edge or Chrome binary found; set ERNEST_BROWSER_BIN to a browser executable")
}

func browserCandidates() []string {
	out := []string{os.Getenv("ERNEST_BROWSER_BIN")}
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles(x86)")
		pf64 := os.Getenv("ProgramFiles")
		local := os.Getenv("LOCALAPPDATA")
		for _, root := range []string{pf, pf64} {
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
			)
		}
		if local != "" {
			out = append(out, filepath.Join(local, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	} else {
		out = append(out,
			"/usr/bin/microsoft-edge", "/usr/bin/microsoft-edge-stable",
			"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		)
	}
	return out
}
