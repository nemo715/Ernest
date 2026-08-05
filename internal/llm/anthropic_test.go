package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
)

func TestAnthropicChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "ak" {
			t.Fatalf("missing x-api-key")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatalf("missing anthropic-version")
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			System    string `json:"system"`
			Stream    bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Model != "claude-test" || req.MaxTokens != 4096 || req.System != "be terse" || req.Stream {
			t.Fatalf("request wrong: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"content": [
				{"type": "text", "text": "Sure, "},
				{"type": "text", "text": "here it is."},
				{"type": "tool_use", "id": "tu_1", "name": "calc", "input": {"expression": "2*3"}}
			],
			"usage": {"input_tokens": 9, "output_tokens": 4},
			"stop_reason": "tool_use"
		}`)
	}))
	defer srv.Close()

	p := NewAnthropic(AnthropicConfig{APIKey: "ak", Model: "claude-test", BaseURL: srv.URL})
	cr, err := p.Chat(context.Background(), ChatRequest{
		Model:    "claude-test",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "be terse"},
			core.NewUserMessage("hi"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Content != "Sure, here it is." {
		t.Fatalf("content = %q", cr.Content)
	}
	if len(cr.ToolCalls) != 1 || cr.ToolCalls[0].Name != "calc" || cr.ToolCalls[0].ID != "tu_1" {
		t.Fatalf("tool calls wrong: %+v", cr.ToolCalls)
	}
	if string(cr.ToolCalls[0].Arguments) != `{"expression":"2*3"}` {
		t.Fatalf("args = %s", cr.ToolCalls[0].Arguments)
	}
	if cr.Usage == nil || cr.Usage.InputTokens != 9 || cr.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", cr.Usage)
	}
}

func TestAnthropicStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"event: content_block_start\n"+
				"data: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_7\",\"name\":\"calc\"}}\n\n"+
				"event: content_block_delta\n"+
				"data: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"expression\\\":\\\"\"}}\n\n"+
				"event: content_block_delta\n"+
				"data: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1+1\\\"}\"}}\n\n"+
				"event: content_block_delta\n"+
				"data: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"one \"}}\n\n"+
				"event: content_block_delta\n"+
				"data: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"moment\"}}\n\n"+
				"event: message_delta\n"+
				"data: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":5,\"output_tokens\":6}}\n\n",
		)
	}))
	defer srv.Close()

	p := NewAnthropic(AnthropicConfig{APIKey: "ak", Model: "m", BaseURL: srv.URL})
	st, err := p.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var content string
	var calls []core.ToolCall
	var finish string
	var usage *core.Usage
	for {
		c, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content += c.Content
		calls = append(calls, c.ToolCalls...)
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if content != "one moment" {
		t.Fatalf("content = %q", content)
	}
	if len(calls) != 1 || calls[0].ID != "tu_7" || calls[0].Name != "calc" {
		t.Fatalf("calls wrong: %+v", calls)
	}
	if string(calls[0].Arguments) != `{"expression":"1+1"}` {
		t.Fatalf("args = %s", calls[0].Arguments)
	}
	if finish != "tool_use" {
		t.Fatalf("finish = %q", finish)
	}
	if usage == nil || usage.InputTokens != 5 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAnthropicToolResultMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type      string `json:"type"`
					ToolUseID string `json:"tool_use_id"`
					Content   string `json:"content"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) != 2 {
			t.Fatalf("messages = %+v", req.Messages)
		}
		// assistant tool_use + user tool_result
		if req.Messages[0].Role != "assistant" || len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Type != "tool_use" {
			t.Fatalf("assistant msg wrong: %+v", req.Messages[0])
		}
		tr := req.Messages[1]
		if tr.Role != "user" || len(tr.Content) != 1 || tr.Content[0].Type != "tool_result" || tr.Content[0].ToolUseID != "tu_1" {
			t.Fatalf("tool_result msg wrong: %+v", tr)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	p := NewAnthropic(AnthropicConfig{APIKey: "ak", Model: "m", BaseURL: srv.URL})
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []core.Message{
			core.NewAssistantMessage("", []core.ToolCall{{ID: "tu_1", Name: "calc", Arguments: []byte(`{"expression":"1"}`)}}),
			core.NewToolMessage("tu_1", "calc", []byte(`{"result":1}`)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicFactory(t *testing.T) {
	if Anthropic("k", "claude-x").Model() != "claude-x" {
		t.Fatal("Anthropic factory wrong")
	}
}
