package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

// CallTool is the "a2a_call" tool: it sends a message to a remote
// agent's A2A endpoint (message/send) and returns the reply text.
var CallTool = core.MustTool[callArgs](
	"a2a_call",
	"Call another agent over the A2A protocol. Args: url (base URL of the target ernest server), agent (agent name), message (text to send).",
	a2aCallRun,
)

type callArgs struct {
	URL      string `json:"url"`
	Agent    string `json:"agent"`
	Message  string `json:"message"`
	TimeoutS int    `json:"timeoutS,omitempty"`
}

func a2aCallRun(ctx context.Context, _ *core.ToolContext, args callArgs) (any, error) {
	if args.URL == "" || args.Agent == "" || args.Message == "" {
		return nil, fmt.Errorf("a2a_call: url, agent and message are required")
	}
	timeout := time.Duration(args.TimeoutS) * time.Second
	if args.TimeoutS <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"messageId": "m_" + newID(),
				"parts":     []map[string]any{{"kind": "text", "text": args.Message}},
			},
		},
	})
	endpoint := args.URL
	if endpoint[len(endpoint)-1] == '/' {
		endpoint = endpoint[:len(endpoint)-1]
	}
	endpoint += "/a2a/" + args.Agent

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a_call %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("a2a_call %s: HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(body), 500))
	}

	var rpc struct {
		Result struct {
			State   string `json:"state"`
			Message struct {
				Role  string `json:"role"`
				Parts []struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, fmt.Errorf("a2a_call: bad response: %v", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("a2a_call: %s (code %d)", rpc.Error.Message, rpc.Error.Code)
	}
	reply := ""
	for _, p := range rpc.Result.Message.Parts {
		if p.Kind == "text" {
			reply += p.Text
		}
	}
	if rpc.Result.State == "failed" {
		return nil, fmt.Errorf("a2a_call: remote agent failed: %s", truncate(reply, 500))
	}
	return reply, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
