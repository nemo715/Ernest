// MCP streamable-HTTP transport (protocol 2025-06-18) for the server
// side: any MCP client that speaks streamable HTTP — Claude Desktop,
// Cursor, IDE plugins, another ernest instance — can reach the same
// agents as `ernest mcp-serve --http :8123`.
package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ServeHTTP implements the streamable-HTTP MCP transport:
//
//   - POST /  accepts a JSON-RPC 2.0 request (or notification) and
//     answers with a single JSON response (application/json), or an
//     SSE stream (text/event-stream) when the client's Accept header
//     asks for one. Notifications get HTTP 202 with no body.
//   - GET  /  opens the SSE stream for server-initiated messages.
//     ernest never pushes messages, so the stream is opened and held
//     open until the client disconnects.
//   - OPTIONS / answers CORS preflight for browser-based clients.
//
// Session management (Mcp-Session-Id) is intentionally not used: the
// server is stateless and every request is self-contained, which keeps
// load-balanced deployments trivial.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization, Mcp-Session-Id")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		s.serveSSEStream(w, r)
	case http.MethodPost:
		s.servePost(w, r)
	default:
		body, _ := json.Marshal(rpcServerError(nil, -32600, "method not allowed: "+r.Method))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write(body)
	}
}

// servePost handles one JSON-RPC request/notification.
func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		s.writeRPC(w, r, rpcServerError(nil, -32700, "parse error: "+err.Error()))
		return
	}
	response, err := s.handleMessage(r.Context(), body)
	if err != nil {
		s.writeRPC(w, r, rpcServerError(nil, -32603, "internal error: "+err.Error()))
		return
	}
	if response == nil {
		// Notification: acknowledged, no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.writeRPC(w, r, response)
}

// writeRPC serializes one JSON-RPC response, honoring the Accept header.
func (s *Server) writeRPC(w http.ResponseWriter, r *http.Request, response any) {
	data, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serveSSEStream holds the GET stream open for server-initiated
// messages. ernest never sends any; keeping the connection open is what
// the transport expects, so clients that subscribe don't spin.
func (s *Server) serveSSEStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	<-r.Context().Done()
}
