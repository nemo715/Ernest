// Package agent is the public API for building and running agents.
// It forwards to the implementation in ernest/internal/agent.
package agent

import (
	"context"

	internal "github.com/nemo715/Ernest/internal/agent"
	internalCore "github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/llm"
)

type (
	Agent      = internal.Agent
	RunOptions = internal.RunOptions
	Hooks      = internal.Hooks
)

// New builds an agent with defaults (mock provider when nil).
func New(name string, provider llm.Provider) *Agent {
	return internal.New(name, provider)
}

// Chat is a convenience wrapper for a synchronous run.
func Chat(ctx context.Context, a *Agent, input string, opts ...RunOptions) (*internalCore.RunResult, error) {
	return a.Chat(ctx, input, opts...)
}

// Stream is a convenience wrapper for a streaming run.
func Stream(ctx context.Context, a *Agent, input string, opts ...RunOptions) (<-chan internalCore.RunEvent, error) {
	return a.Stream(ctx, input, opts...)
}
