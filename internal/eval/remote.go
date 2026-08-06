package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

// HTTPRunner runs scenarios against a LIVE ernest server over its HTTP
// API (POST /api/chat, SSE). It is the transport of `ernest replay`:
// the same suite, same assertions, same baselines — but the agent under
// test is the one deployed in production. The judge provider is the
// local config's provider (replay runs offline evals in the same model
// family as the deployment).
type HTTPRunner struct {
	Endpoint      string // base URL, e.g. http://prod:9090
	Agent         string // agent name on the server
	Client        *http.Client
	JudgeProvider llm.Provider // judge scoring provider (local config)
	Timeout       time.Duration
}

// RunScenario streams one input through the server's /api/chat and
// collects the same events AgentRunner does.
func (r HTTPRunner) RunScenario(ctx context.Context, input string) (*Outcome, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body, err := json.Marshal(map[string]any{
		"agent":      r.Agent,
		"input":      input,
		"skipMemory": true,
	})
	if err != nil {
		return nil, err
	}
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.Endpoint, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("endpoint returned %s: %s", resp.Status, truncate(string(msg), 300))
	}

	var o Outcome
	var res *core.RunResult
	var metrics *core.RunMetrics
	runErr := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		ev, err := core.DecodeEvent([]byte(strings.TrimPrefix(line, "data: ")))
		if err != nil {
			continue // ignore malformed frames; the result event decides
		}
		switch {
		case ev.ToolCall != nil:
			o.ToolCalls = append(o.ToolCalls, *ev.ToolCall)
		case ev.ToolResult != nil:
			o.ToolResults = append(o.ToolResults, *ev.ToolResult)
		case ev.Metrics != nil:
			metrics = ev.Metrics
		case ev.Result != nil:
			res = ev.Result
		case ev.Type == core.EventRunError:
			// A failed run is drift to report, not an abort: remember the
			// error, let the following result event (status failed) become
			// a failed outcome, and fail via the assertions below.
			runErr = ev.Error
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	if res == nil {
		if runErr != "" {
			return nil, fmt.Errorf("run failed remotely: %s", truncate(runErr, 300))
		}
		return nil, fmt.Errorf("endpoint streamed no result event")
	}
	o.Output = res.Output
	o.Status = string(res.Status)
	o.Error = res.Error
	if o.Error == "" && runErr != "" {
		o.Error = runErr
	}
	o.Usage = res.Usage
	o.DurationMS = time.Since(start).Milliseconds()
	if o.DurationMS == 0 {
		o.DurationMS = res.DurationMS
	}
	if metrics != nil {
		o.CostCents = metrics.CostCents
	}
	return &o, nil
}

// Provider returns the judge scoring provider.
func (r HTTPRunner) Provider() llm.Provider { return r.JudgeProvider }

// Model returns the deployed agent's model by asking the server
// (GET /api/agents). Empty on failure — the header is informational.
func (r HTTPRunner) Model(ctx context.Context) string {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.Endpoint, "/")+"/api/agents", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var agents []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&agents); err != nil {
		return ""
	}
	for _, a := range agents {
		if a.Name == r.Agent {
			return a.Model
		}
	}
	return ""
}
