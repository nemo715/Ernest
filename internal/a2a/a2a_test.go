package a2a

import (
	"context"
	"testing"
	"time"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/llm"
)

func testA2AServer(t *testing.T) *Server {
	t.Helper()
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{Content: "Hello from the far side", FinishReason: "stop"},
		},
	})
	a := agent.New("far", p)
	a.Description = "The far agent"
	return NewServer([]*agent.Agent{a})
}

func callRPC(t *testing.T, s *Server, agentName, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"` + method + `","params":` + params + `}`
	resp, err := s.HandleJSONRPC(context.Background(), agentName, []byte(body))
	if err != nil {
		t.Fatalf("HandleJSONRPC: %v", err)
	}
	m, ok := resp.(map[string]any)
	if !ok {
		t.Fatalf("resp type %T", resp)
	}
	return m
}

func TestA2AInitialize(t *testing.T) {
	s := testA2AServer(t)
	resp := callRPC(t, s, "far", "initialize", `{}`)
	if resp["error"] != nil {
		t.Fatalf("error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("result = %v", result)
	}
}

func TestA2ACard(t *testing.T) {
	s := testA2AServer(t)
	card, ok := s.AgentCard("far", "http://example.com")
	if !ok {
		t.Fatal("card not found")
	}
	if card.URL != "http://example.com/a2a/far" || card.Description != "The far agent" {
		t.Fatalf("card = %+v", card)
	}
	if len(s.AllCards("http://example.com")) != 1 {
		t.Fatal("expected 1 card")
	}
}

func TestA2AMessageSend(t *testing.T) {
	s := testA2AServer(t)
	resp := callRPC(t, s, "far", "message/send", `{"message":{"role":"user","messageId":"m1","parts":[{"kind":"text","text":"hi"}]}}`)
	result, _ := resp["result"].(map[string]any)
	if result["state"] != MessageState(StateCompleted) {
		t.Fatalf("result = %v", result)
	}
	msg, _ := result["message"].(Message)
	if len(msg.Parts) != 1 || msg.Parts[0].Text != "Hello from the far side" {
		t.Fatalf("parts = %v", msg.Parts)
	}
}

func TestA2AUnknownAgent(t *testing.T) {
	s := testA2AServer(t)
	resp := callRPC(t, s, "nope", "message/send", `{"message":{"role":"user","messageId":"m1","parts":[{"kind":"text","text":"hi"}]}}`)
	errObj, _ := resp["error"].(map[string]any)
	code, _ := errObj["code"].(int)
	if code != -32002 {
		t.Fatalf("resp = %v", resp)
	}
}

func TestA2ATaskLifecycle(t *testing.T) {
	s := testA2AServer(t)
	resp := callRPC(t, s, "far", "tasks/send", `{"message":{"role":"user","messageId":"m1","parts":[{"kind":"text","text":"hi"}]}}`)
	result, _ := resp["result"].(*Task)
	taskID := result.ID
	if taskID == "" {
		t.Fatalf("task = %v", result)
	}

	// Poll tasks/get until completed. The worker runs in its own
	// goroutine, so the loop must yield between polls: a spin loop can
	// exhaust all 100 iterations before the worker is ever scheduled.
	for i := 0; i < 100; i++ {
		time.Sleep(5 * time.Millisecond)
		g := callRPC(t, s, "far", "tasks/get", `{"id":"`+taskID+`"}`)
		tsk, _ := g["result"].(*Task)
		if tsk == nil {
			t.Fatalf("tasks/get result = %v", g)
		}
		if tsk.State == StateCompleted {
			if len(tsk.Artifacts) != 1 {
				t.Fatalf("artifacts = %v", tsk.Artifacts)
			}
			return
		}
		if tsk.State == StateFailed || tsk.State == StateCanceled {
			t.Fatalf("task state = %s", tsk.State)
		}
	}
	t.Fatal("task never completed")
}

func TestA2AErrors(t *testing.T) {
	s := testA2AServer(t)
	resp := callRPC(t, s, "far", "bogus", `{}`)
	if resp["error"] == nil {
		t.Fatal("expected error")
	}
	resp = callRPC(t, s, "far", "message/send", `{"message":{"role":"user","messageId":"m1","parts":[{"kind":"file"}]}}`)
	if resp["error"] == nil {
		t.Fatal("expected error for empty text")
	}
	// Malformed JSON yields a parse error response, not a Go error.
	_, err := s.HandleJSONRPC(context.Background(), "far", []byte(`{bad`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}
