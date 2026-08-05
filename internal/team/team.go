// Package team implements multi-agent teams: a leader agent delegates
// tasks to specialist members through a built-in delegate tool.
package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ernest/internal/agent"
	"ernest/internal/core"
)

// Team orchestrates a leader and its members. The leader keeps full
// autonomy: it decides when (and to whom) to delegate, using the
// automatically injected `delegate` tool.
type Team struct {
	Name         string
	Description  string
	Leader       *agent.Agent
	Members      []*agent.Agent
	Instructions string // extra context appended to the leader's instructions
	MaxIterations int
}

// New builds a team from a leader and members.
func New(name string, leader *agent.Agent, members ...*agent.Agent) *Team {
	return &Team{Name: name, Leader: leader, Members: members, MaxIterations: 8}
}

// RunOptions mirror agent.RunOptions.
type RunOptions = agent.RunOptions

// delegateArgs is the schema for the delegate tool.
type delegateArgs struct {
	Member string `json:"member" jsonschema:"Name of the team member to delegate to"`
	Task   string `json:"task" jsonschema:"The task to hand to the member"`
}

// leaderAgent builds the team's leader copy with the delegate tool and
// team instructions injected.
func (t *Team) leaderAgent() (*agent.Agent, error) {
	if t.Leader == nil {
		return nil, core.NewError(core.KindAgent, "team "+t.Name+" has no leader")
	}
	// Build a fresh leader: Agent contains a sync.Mutex, so copying the
	// struct value (leader := *t.Leader) is illegal (vet copylocks) and
	// would also share approval state with the caller's agent.
	leader := &agent.Agent{
		Name:          t.Leader.Name,
		Description:   t.Leader.Description,
		Instructions:  t.Leader.Instructions,
		Provider:      t.Leader.Provider,
		Memory:        t.Leader.Memory,
		Store:         t.Leader.Store,
		Knowledge:     t.Leader.Knowledge,
		MaxIterations: t.Leader.MaxIterations,
		Temperature:   t.Leader.Temperature,
		MaxTokens:     t.Leader.MaxTokens,
		Stop:          t.Leader.Stop,
		Hooks:         t.Leader.Hooks,
	}
	// Do not mutate the caller's tool slice.
	tools := make([]*core.Tool, 0, len(t.Leader.Tools)+1)
	tools = append(tools, t.Leader.Tools...)

	members := map[string]*agent.Agent{}
	var names []string
	for _, m := range t.Members {
		if m == nil || m.Name == "" {
			continue
		}
		members[m.Name] = m
		names = append(names, fmt.Sprintf("- %s: %s", m.Name, m.Description))
	}
	if len(members) == 0 {
		return nil, core.NewError(core.KindAgent, "team "+t.Name+" has no members")
	}

	delegateTool, err := core.NewTool[delegateArgs]("delegate", "Delegate a task to a specialist team member", func(ctx context.Context, tc *core.ToolContext, args delegateArgs) (any, error) {
		member, ok := members[args.Member]
		if !ok {
			return nil, core.NewError(core.KindTool, fmt.Sprintf("unknown member %q (available: %s)", args.Member, strings.Join(names, ", ")))
		}
		// Members keep their own per-team sessions.
		sessionID := tc.RunID + ":member:" + member.Name
		if tc.Emit != nil {
			tc.Emit(core.RunEvent{Type: core.EventDelegateStart, RunID: tc.RunID, Agent: member.Name, Data: json.RawMessage(fmt.Sprintf(`{"task":%q}`, args.Task))})
		}
		result, err := member.Chat(ctx, args.Task, agent.RunOptions{SessionID: sessionID})
		if err != nil {
			return nil, err
		}
		if tc.Emit != nil {
			tc.Emit(core.RunEvent{Type: core.EventDelegateEnd, RunID: tc.RunID, Agent: member.Name, Data: json.RawMessage(fmt.Sprintf(`{"task":%q,"output":%q}`, args.Task, result.Output))})
		}
		return map[string]any{
			"member": member.Name,
			"task":   args.Task,
			"output": result.Output,
			"status": string(result.Status),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, delegateTool)
	leader.Tools = tools
	if t.MaxIterations > 0 {
		leader.MaxIterations = t.MaxIterations
	}
	leader.Instructions = strings.TrimSpace(leader.Instructions) +
		"\n\nYou are the leader of a team. Team members:\n" + strings.Join(names, "\n") +
		"\n\nUse the `delegate` tool to hand tasks to members. Wait for their results, " +
		"synthesise them, and answer the user. Never claim work you did not do." +
		(t.Instructions)
	return leader, nil
}

// Chat runs the team synchronously.
func (t *Team) Chat(ctx context.Context, input string, opts ...RunOptions) (*core.RunResult, error) {
	leader, err := t.leaderAgent()
	if err != nil {
		return nil, err
	}
	return leader.Chat(ctx, input, opts...)
}

// Stream runs the team and returns an event channel.
func (t *Team) Stream(ctx context.Context, input string, opts ...RunOptions) (<-chan core.RunEvent, error) {
	leader, err := t.leaderAgent()
	if err != nil {
		return nil, err
	}
	return leader.Stream(ctx, input, opts...)
}

// Resume continues an interrupted run with a human decision.
func (t *Team) Resume(ctx context.Context, decision core.ApprovalDecision) (*core.RunResult, error) {
	leader, err := t.leaderAgent()
	if err != nil {
		return nil, err
	}
	return leader.Resume(ctx, decision)
}
