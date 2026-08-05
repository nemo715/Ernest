package llm

import (
	"context"
	"io"
	"testing"

	"ernest/internal/core"
)

func TestMockChatScripted(t *testing.T) {
	m := NewMock(MockConfig{
		Script: []MockTurn{
			{Content: "first", FinishReason: "stop"},
			{Content: "second", FinishReason: "stop"},
		},
		Default: MockTurn{Content: "default"},
	})
	ctx := context.Background()
	req := ChatRequest{Model: "mock-1", Messages: []core.Message{core.NewUserMessage("hi")}}

	c1, err := m.Chat(ctx, req)
	if err != nil || c1.Content != "first" {
		t.Fatalf("turn1 = %+v, %v", c1, err)
	}
	c2, err := m.Chat(ctx, req)
	if err != nil || c2.Content != "second" {
		t.Fatalf("turn2 = %+v, %v", c2, err)
	}
	c3, err := m.Chat(ctx, req) // script exhausted -> Default
	if err != nil || c3.Content != "default" {
		t.Fatalf("turn3 = %+v, %v", c3, err)
	}
	if m.CallCount() != 3 {
		t.Fatalf("call count = %d, want 3", m.CallCount())
	}
	if len(m.Requests) != 3 || m.Requests[0].Model != "mock-1" {
		t.Fatalf("requests not recorded: %+v", m.Requests)
	}
}

func TestMockStreamDeltas(t *testing.T) {
	m := NewMock(MockConfig{
		Stream: true,
		Script: []MockTurn{{Content: "hey", FinishReason: "stop"}},
	})
	s, err := m.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	seenFinal := false
	for {
		c, err := s.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content += c.Content
		if len(c.ToolCalls) > 0 || c.FinishReason != "" {
			seenFinal = true
		}
	}
	if content != "hey" || !seenFinal {
		t.Fatalf("stream wrong: content=%q seenFinal=%v", content, seenFinal)
	}
}

func TestMockToolCallTurn(t *testing.T) {
	m := NewMock(MockConfig{
		Script: []MockTurn{{
			ToolCalls:    []core.ToolCall{{ID: "c1", Name: "calc", Arguments: []byte(`{"expression":"1+1"}`)}},
			FinishReason: "tool_calls",
		}},
	})
	cr, err := m.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cr.ToolCalls) != 1 || cr.ToolCalls[0].Name != "calc" || cr.FinishReason != "tool_calls" {
		t.Fatalf("tool turn wrong: %+v", cr)
	}
}

func TestMockEmbedDeterministic(t *testing.T) {
	m := NewMock(MockConfig{EmbedDim: 4})
	ctx := context.Background()
	a1, err := m.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := m.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a1) != 1 || len(a1[0]) != 4 {
		t.Fatalf("embed dim wrong: %d", len(a1[0]))
	}
	for i := range a1[0] {
		if a1[0][i] != a2[0][i] {
			t.Fatal("embed must be deterministic")
		}
	}
	// Different text must give a different vector.
	b, err := m.Embed(ctx, []string{"goodbye"})
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for i := range a1[0] {
		if a1[0][i] != b[0][i] {
			same = false
		}
	}
	if same {
		t.Fatal("different texts must embed differently")
	}
}

func TestMockEmbedErr(t *testing.T) {
	m := NewMock(MockConfig{EmbedErr: core.NewError(core.KindProvider, "embed down")})
	if _, err := m.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("embed error must propagate")
	}
}
