package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
)

func TestGeminiChat(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Query().Get("key") != "gk" {
			t.Fatalf("missing api key in query")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "Result: "},
						{"functionCall": {"name": "calc", "args": {"expression": "6*7"}}}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 4, "candidatesTokenCount": 2}
		}`)
	}))
	defer srv.Close()

	p := NewGemini(GeminiConfig{APIKey: "gk", Model: "gemini-test", BaseURL: srv.URL})
	cr, err := p.Chat(context.Background(), ChatRequest{Model: "gemini-test"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/models/gemini-test:generateContent") {
		t.Fatalf("path = %s", gotPath)
	}
	if cr.Content != "Result: " {
		t.Fatalf("content = %q", cr.Content)
	}
	if len(cr.ToolCalls) != 1 || cr.ToolCalls[0].Name != "calc" {
		t.Fatalf("tool calls wrong: %+v", cr.ToolCalls)
	}
	if string(cr.ToolCalls[0].Arguments) != `{"expression": "6*7"}` {
		t.Fatalf("args = %s", cr.ToolCalls[0].Arguments)
	}
	if cr.Usage == nil || cr.Usage.InputTokens != 4 || cr.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", cr.Usage)
	}
}

func TestGeminiStream(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Two \"}]}}]}\n\n"+
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"plus \"}]}}]}\n\n"+
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"calc\",\"args\":{\"expression\":\"2+2\"}}}]},\"finishReason\":\"STOP\"}]}\n\n",
		)
	}))
	defer srv.Close()

	p := NewGemini(GeminiConfig{APIKey: "gk", Model: "gemini-test", BaseURL: srv.URL})
	st, err := p.Stream(context.Background(), ChatRequest{Model: "gemini-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !strings.Contains(gotPath, ":streamGenerateContent") || !strings.Contains(gotPath, "alt=sse") {
		t.Fatalf("stream path = %s", gotPath)
	}
	var content string
	var calls []core.ToolCall
	var finish string
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
	}
	if content != "Two plus " {
		t.Fatalf("content = %q", content)
	}
	if len(calls) != 1 || calls[0].Name != "calc" || string(calls[0].Arguments) != `{"expression":"2+2"}` {
		t.Fatalf("calls wrong: %+v", calls)
	}
	if finish != "STOP" {
		t.Fatalf("finish = %q", finish)
	}
}

func TestGeminiToolResultMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	p := NewGemini(GeminiConfig{APIKey: "gk", Model: "m", BaseURL: srv.URL})
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []core.Message{
			core.NewAssistantMessage("", []core.ToolCall{{ID: "c1", Name: "calc", Arguments: []byte(`{"expression":"1"}`)}}),
			core.NewToolMessage("c1", "calc", []byte(`{"result":1}`)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeminiFactory(t *testing.T) {
	if Gemini("k", "gemini-2.0-flash").Model() != "gemini-2.0-flash" {
		t.Fatal("Gemini factory wrong")
	}
}
