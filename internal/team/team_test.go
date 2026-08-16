package team

import (
	"context"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

func TestTeamChatDelegatesAndSynthesises(t *testing.T) {
	leaderP := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "d1", Name: "delegate", Arguments: []byte(`{"member":"researcher","task":"research x"}`)}}, FinishReason: "tool_calls"},
		{Content: "Final answer", FinishReason: "stop"},
	}})
	memberP := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "Research output", FinishReason: "stop"}}})
	leader := agent.New("lead", leaderP)
	leader.Description = "Coordinator agent"
	researcher := agent.New("researcher", memberP)
	researcher.Description = "Research specialist"

	tm := New("team", leader, researcher)
	res, err := tm.Chat(context.Background(), "do research")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Output != "Final answer" {
		t.Fatalf("output = %q", res.Output)
	}
	// The member was delegated exactly once.
	if memberP.CallCount() != 1 {
		t.Fatalf("member call count = %d", memberP.CallCount())
	}
	// The delegate result is in the leader's tool history.
	found := false
	for _, m := range res.Messages {
		if m.Role == core.RoleTool && m.Name == "delegate" && strings.Contains(m.Content, "Research output") {
			found = true
		}
	}
	if !found {
		t.Fatalf("delegate result missing: %+v", res.Messages)
	}
}

func TestDelegateEvents(t *testing.T) {
	leaderP := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "d1", Name: "delegate", Arguments: []byte(`{"member":"writer","task":"write intro"}`)}}, FinishReason: "tool_calls"},
		{Content: "All done", FinishReason: "stop"},
	}})
	writer := agent.New("writer", llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "intro written", FinishReason: "stop"}}}))
	tm := New("content", agent.New("lead", leaderP), writer)

	ch, err := tm.Stream(context.Background(), "help")
	if err != nil {
		t.Fatal(err)
	}
	var types []core.EventType
	var delegateAgent string
	for ev := range ch {
		types = append(types, ev.Type)
		if ev.Type == core.EventDelegateStart {
			delegateAgent = ev.Agent
		}
	}
	startIdx, endIdx := -1, -1
	for i, ty := range types {
		switch ty {
		case core.EventDelegateStart:
			startIdx = i
		case core.EventDelegateEnd:
			endIdx = i
		}
	}
	if startIdx < 0 || endIdx < 0 || endIdx < startIdx {
		t.Fatalf("delegate events missing/out of order: %v", types)
	}
	if delegateAgent != "writer" {
		t.Fatalf("delegate agent = %q", delegateAgent)
	}
	if types[len(types)-1] != core.EventRunComplete {
		t.Fatalf("last event = %s", types[len(types)-1])
	}
}

func TestDelegateUnknownMember(t *testing.T) {
	leader := agent.New("lead", llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "d1", Name: "delegate", Arguments: []byte(`{"member":"ghost","task":"x"}`)}}, FinishReason: "tool_calls"},
		{Content: "I cannot do that", FinishReason: "stop"},
	}}))
	tm := New("t", leader, agent.New("real", llm.NewMock(llm.MockConfig{})))
	res, err := tm.Chat(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	// The tool error is fed back to the model as a tool result.
	found := false
	for _, m := range res.Messages {
		if m.Role == core.RoleTool && m.Name == "delegate" && strings.Contains(m.Content, "unknown member") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown-member feedback missing: %+v", res.Messages)
	}
}

func TestTeamRequiresLeaderAndMembers(t *testing.T) {
	lead := agent.New("lead", llm.NewMock(llm.MockConfig{}))
	tm := New("no-members", lead)
	if _, err := tm.Chat(context.Background(), "hi"); err == nil {
		t.Fatal("team without members must error")
	}
	tm = New("no-leader", nil, lead)
	if _, err := tm.Chat(context.Background(), "hi"); err == nil {
		t.Fatal("team without leader must error")
	}
}

func TestLeaderInstructionsIncludeMembers(t *testing.T) {
	leaderP := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}})
	leader := agent.New("lead", leaderP)
	leader.Instructions = "You coordinate."
	member := agent.New("analyst", llm.NewMock(llm.MockConfig{}))
	member.Description = "Analysis specialist"
	tm := New("t", leader, member)
	if _, err := tm.Chat(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	system := ""
	for _, m := range leaderP.Requests[0].Messages {
		if m.Role == core.RoleSystem {
			system = m.Content
		}
	}
	for _, want := range []string{"analyst", "Analysis specialist", "delegate", "You coordinate."} {
		if !strings.Contains(system, want) {
			t.Fatalf("instructions missing %q: %q", want, system)
		}
	}
}

func TestTeamDoesNotMutateLeader(t *testing.T) {
	leaderP := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{{ID: "d1", Name: "delegate", Arguments: []byte(`{"member":"m","task":"t"}`)}}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}})
	leader := agent.New("lead", leaderP)
	member := agent.New("m", llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "ok", FinishReason: "stop"}}}))
	origTools := len(leader.Tools)
	origInstr := leader.Instructions

	tm := New("t", leader, member)
	if _, err := tm.Chat(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(leader.Tools) != origTools {
		t.Fatalf("leader tools mutated: %d -> %d", origTools, len(leader.Tools))
	}
	if leader.Instructions != origInstr {
		t.Fatal("leader instructions mutated")
	}
}

func TestTeamSequentialChainsOutputs(t *testing.T) {
	p1 := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "step one output", FinishReason: "stop"}}})
	p2 := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "step two output", FinishReason: "stop"}}})
	first := agent.New("first", p1)
	second := agent.New("second", p2)

	tm := New("chain", nil, first, second)
	tm.Process = "sequential"

	res, err := tm.Chat(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	// The final output is the last member's output.
	if res.Output != "step two output" {
		t.Fatalf("output = %q", res.Output)
	}
	// The second member received the first member's output as input.
	if len(p2.Requests) != 1 || len(p2.Requests[0].Messages) == 0 {
		t.Fatalf("second member requests = %+v", p2.Requests)
	}
	var user string
	for _, m := range p2.Requests[0].Messages {
		if m.Role == core.RoleUser {
			user = m.Content
		}
	}
	if user != "step one output" {
		t.Fatalf("second member input = %q, want %q", user, "step one output")
	}
	// The leader model was never called in sequential mode.
	if got := res.Metadata["process"]; got != "sequential" {
		t.Fatalf("process metadata = %v", got)
	}
}

func TestTeamSequentialStreamsDelegateEvents(t *testing.T) {
	first := agent.New("first", llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "a", FinishReason: "stop"}}}))
	second := agent.New("second", llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{{Content: "b", FinishReason: "stop"}}}))
	tm := New("chain", nil, first, second)
	tm.Process = "sequential"

	ch, err := tm.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var types []core.EventType
	var starts, ends int
	var lastResult *core.RunResult
	for ev := range ch {
		types = append(types, ev.Type)
		switch ev.Type {
		case core.EventDelegateStart:
			starts++
		case core.EventDelegateEnd:
			ends++
		case core.EventRunComplete:
			lastResult = ev.Result
		}
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("delegate events: starts=%d ends=%d (%v)", starts, ends, types)
	}
	if len(types) == 0 || types[0] != core.EventRunStart {
		t.Fatalf("first event = %v", types)
	}
	if types[len(types)-1] != core.EventRunComplete {
		t.Fatalf("last event = %v", types[len(types)-1])
	}
	if lastResult == nil || lastResult.Output != "b" {
		t.Fatalf("final result = %+v", lastResult)
	}
	// Metadata carries the member names in declaration order.
	names, ok := lastResult.Metadata["members"].([]string)
	if !ok || len(names) != 2 || names[0] != "first" || names[1] != "second" {
		t.Fatalf("members metadata = %+v", lastResult.Metadata["members"])
	}
}

func TestTeamSequentialNoMembers(t *testing.T) {
	tm := New("empty", nil)
	tm.Process = "sequential"
	if _, err := tm.Chat(context.Background(), "hi"); err == nil {
		t.Fatal("sequential team without members must error")
	}
}
