// Package server is the public API for the ernest HTTP server: SSE chat,
// WebSocket, approvals, sessions, traces, audit and A2A routes.
// It forwards to the implementation in ernest/internal/server.
package server

import internal "github.com/nemo715/Ernest/internal/server"

type (
	Options = internal.Options
	Server  = internal.Server
)

// New builds a server from options.
func New(opts Options) (*Server, error) {
	return internal.New(opts)
}
