// Package team is the public API for multi-agent teams: a leader that
// delegates to specialist members via the injected delegate tool.
// It forwards to the implementation in ernest/internal/team.
package team

import (
	"context"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	internal "github.com/nemo715/Ernest/internal/team"
)

type (
	Team       = internal.Team
	RunOptions = agent.RunOptions
)

// New assembles a team: leader + members. The leader gets the delegate
// tool injected automatically.
func New(name string, leader *agent.Agent, members ...*agent.Agent) *Team {
	return internal.New(name, leader, members...)
}

// Chat runs the team synchronously.
func Chat(ctx context.Context, t *Team, input string, opts ...RunOptions) (*core.RunResult, error) {
	return t.Chat(ctx, input, opts...)
}

// Stream runs the team and streams its events.
func Stream(ctx context.Context, t *Team, input string, opts ...RunOptions) (<-chan core.RunEvent, error) {
	return t.Stream(ctx, input, opts...)
}
