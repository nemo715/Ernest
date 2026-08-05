package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

// httpTransport implements MCP's streamable HTTP transport: JSON-RPC over
// POST with Accept: application/json, text/event-stream, and session
// identity via the Mcp-Session-Id header.
type httpTransport struct {
	endpoint string
	client   *http.Client
	mu       sync.Mutex
	session  string // Mcp-Session-Id established by the server
}

func newHTTPTransport(endpoint string) (*httpTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, core.NewError(core.KindMCP, "mcp: invalid endpoint "+endpoint)
	}
	return &httpTransport{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func setMCPHeaders(h http.Header, session string) {
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	h.Set("MCP-Protocol-Version", ProtocolVersion)
	if session != "" {
		h.Set("Mcp-Session-Id", session)
	}
}

func (t *httpTransport) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	body := marshalRPC(id, method, params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: "+err.Error(), err)
	}
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	setMCPHeaders(req.Header, session)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: request failed: "+err.Error(), err)
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, core.NewError(core.KindMCP, fmt.Sprintf("mcp: http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg))))
	}

	ct := resp.Header.Get("Content-Type")
	var data []byte
	if strings.Contains(ct, "text/event-stream") {
		data, err = firstMessageEvent(resp.Body)
		if err != nil {
			return nil, err
		}
	} else {
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, core.NewError(core.KindMCP, "mcp: read response: "+err.Error(), err)
		}
	}
	if len(data) == 0 {
		return nil, nil // 202 Accepted with empty body
	}
	var rpc rpcResponse
	if err := json.Unmarshal(data, &rpc); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: invalid json-rpc response: "+err.Error(), err)
	}
	if rpc.Error != nil {
		return nil, rpcResultError(rpc.Error)
	}
	return rpc.Result, nil
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(marshalNotification(method, params)))
	if err != nil {
		return core.NewError(core.KindMCP, "mcp: "+err.Error(), err)
	}
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	setMCPHeaders(req.Header, session)
	resp, err := t.client.Do(req)
	if err != nil {
		return core.NewError(core.KindMCP, "mcp: notification failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return core.NewError(core.KindMCP, fmt.Sprintf("mcp: notification http %d", resp.StatusCode))
	}
	return nil
}

// firstMessageEvent reads an SSE stream and returns the payload of the
// first `event: message`. Comments, pings and other event types are
// skipped; an `event: error` aborts the call.
func firstMessageEvent(r io.Reader) ([]byte, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var event string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			switch event {
			case "error":
				return nil, core.NewError(core.KindMCP, "mcp: sse error: "+strings.TrimSpace(string(data)))
			case "message":
				if len(data) > 0 {
					return data, nil
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
			// comment
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ")
			data = append(data, chunk...)
			data = append(data, '\n')
		}
	}
	if err := sc.Err(); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: sse read: "+err.Error(), err)
	}
	return nil, core.NewError(core.KindMCP, "mcp: sse stream ended without a message event")
}

func (t *httpTransport) close() error {
	// Best-effort session termination (streamable HTTP DELETE).
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return nil
	}
	setMCPHeaders(req.Header, session)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil // best effort
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return nil
}
