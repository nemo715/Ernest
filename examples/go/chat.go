// Example: run an ernest agent with streaming events and a
// human-in-the-loop approval, then resume the run.
//
// Build/run from the repository root (no external deps, uses the
// deterministic mock provider — swap in llm.NewOpenAICompat /
// NewAnthropic / NewGemini for a real model):
//
//	go run ./examples/go/chat.go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"ernest/internal/agent"
	"ernest/internal/core"
	"ernest/internal/llm"
	"ernest/internal/storage"
)

// emailArgs is the JSON Schema source for the send_email tool (the
// schema is derived from the Go struct via reflection).
type emailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func main() {
	ctx := context.Background()

	// 1. Agent with the deterministic mock provider. The script is one
	//    response per model call; turn 1 requests the tool.
	provider := llm.NewMock(llm.MockConfig{
		Model: "mock-1",
		Script: []llm.MockTurn{
			{
				Content: "I'll prepare the email and ask for your approval.",
				ToolCalls: []core.ToolCall{{
					ID:        "call_1",
					Name:      "send_email",
					Arguments: json.RawMessage(`{"to":"team@example.com","subject":"Ernest demo","body":"Automated summary"}`),
				}},
			},
			{Content: "The email was sent successfully.", Usage: &core.Usage{InputTokens: 412, OutputTokens: 96}},
		},
		Stream: true,
	})
	ag := agent.New("assistant", provider)

	// A HITL pause replays the stored session on resume, so the agent
	// needs a session store (in-memory here; sqlite/postgres in prod).
	ag.Store = storage.NewInMemoryStore()

	// 2. Register a tool. Calling tc.RequestApproval pauses the run until
	//    a human decides; on resume it returns nil (approved) or an error
	//    (rejected).
	sendEmail, err := core.NewTool("send_email", "Send an email message", func(ctx context.Context, tc *core.ToolContext, args emailArgs) (any, error) {
		if err := tc.RequestApproval("send_email", fmt.Sprintf("Send an email to %s with subject %q?", args.To, args.Subject), map[string]any{"to": args.To}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "to": args.To}, nil
	})
	if err != nil {
		panic(err)
	}
	ag.Tools = []*core.Tool{sendEmail}

	// 3. Stream the run: tokens, tool call, approval pause, completion.
	ch, err := ag.Stream(ctx, "send a summary email")
	if err != nil {
		panic(err)
	}
	var result *core.RunResult
	for ev := range ch {
		switch ev.Type {
		case core.EventMessageDelta:
			fmt.Print(ev.Delta)
		case core.EventToolCall:
			fmt.Printf("\n[tool] %s arguments=%s\n", ev.ToolCall.Name, ev.ToolCall.Arguments)
		case core.EventApprovalRequest:
			fmt.Printf("[approval] %s: %s\n", ev.Approval.ID, ev.Approval.Summary)
		case core.EventRunComplete:
			result = ev.Result
		}
	}
	fmt.Println()

	// 4. Resolve the HITL pause, if the run is waiting.
	if result != nil && result.Status == core.RunStatusAwaitingApproval {
		id := result.Approvals[0].ID
		fmt.Printf("approving %s...\n", id)
		resume, err := ag.StreamResume(ctx, core.ApprovalDecision{ApprovalID: id, Approved: true, Note: "looks good"})
		if err != nil {
			panic(err)
		}
		for ev := range resume {
			switch ev.Type {
			case core.EventApprovalResolved:
				fmt.Printf("[resolved] %s → %s\n", ev.Approval.ID, ev.Approval.Status)
			case core.EventToolResult:
				fmt.Printf("[tool result] %s\n", ev.ToolResult.Content)
			case core.EventRunComplete:
				result = ev.Result
			}
		}
	}

	if result != nil {
		usage := "n/a"
		if result.Usage != nil {
			usage = fmt.Sprintf("%d in / %d out", result.Usage.InputTokens, result.Usage.OutputTokens)
		}
		fmt.Printf("status=%s duration=%dms usage=%s\n", result.Status, result.DurationMS, usage)
	}
}
