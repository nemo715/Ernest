package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
)

// oaiTestServer is a scriptable OpenAI-compatible endpoint.
type oaiTestServer struct {
	t       *testing.T
	srv     *httptest.Server
	lastReq oaiChatRequest
	// chatBody is returned by POST /chat/completions (non-stream).
	chatBody string
	// streamBody is the SSE text returned by POST /chat/completions (stream).
	streamBody string
	status     int
	embedBody  string
	embedPath  string
}

func newOAITestServer(t *testing.T) *oaiTestServer {
	s := &oaiTestServer{t: t, status: 200}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &s.lastReq)
			w.Header().Set("Content-Type", "application/json")
			if s.streamBody != "" {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, s.streamBody)
				return
			}
			w.WriteHeader(s.status)
			fmt.Fprint(w, s.chatBody)
		case strings.HasSuffix(r.URL.Path, "/embeddings") || strings.HasSuffix(r.URL.Path, "/api/embed"):
			s.embedPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			_ = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.status)
			fmt.Fprint(w, s.embedBody)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	return s
}

func (s *oaiTestServer) close() { s.srv.Close() }

func oaiProvider(t *testing.T, s *oaiTestServer, style string) Provider {
	cfg := OpenAICompatConfig{
		BaseURL:    s.srv.URL,
		APIKey:     "test-key",
		Model:      "test-model",
		EmbedStyle: style,
		EmbedModel: "embed-1",
	}
	return NewOpenAICompat(cfg)
}

func TestOpenAICompatChat(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.chatBody = `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "The answer is 42",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "calc", "arguments": "{\"expression\":\"6*7\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 7}
	}`
	p := oaiProvider(t, s, "")
	cr, err := p.Chat(context.Background(), ChatRequest{
		Model:    "test-model",
		Messages: []core.Message{core.NewUserMessage("what is 6*7?")},
		Tools:    []*core.Tool{core.Calculator},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Content != "The answer is 42" {
		t.Fatalf("content = %q", cr.Content)
	}
	if len(cr.ToolCalls) != 1 || cr.ToolCalls[0].Name != "calc" || cr.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls wrong: %+v", cr.ToolCalls)
	}
	// Response arguments come over the wire as a JSON string; they must be
	// unquoted raw JSON for the tool layer (not `\"{\\\"expression...`).
	if got := string(cr.ToolCalls[0].Arguments); got != `{"expression":"6*7"}` {
		t.Fatalf("tool call arguments = %q, want raw JSON", got)
	}
	if cr.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", cr.FinishReason)
	}
	if cr.Usage == nil || cr.Usage.InputTokens != 12 || cr.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", cr.Usage)
	}
	// Request shape: stream=false, auth header, tool definitions attached.
	if s.lastReq.Stream {
		t.Fatal("expected non-streaming request")
	}
	if len(s.lastReq.Tools) != 1 || s.lastReq.Tools[0].Function.Name != "calculator" {
		t.Fatalf("tools in request wrong: %+v", s.lastReq.Tools)
	}
	if len(s.lastReq.Messages) != 1 || s.lastReq.Messages[0].Role != "user" {
		t.Fatalf("messages wrong: %+v", s.lastReq.Messages)
	}
}

// Regression: follow-up requests must re-send tool calls with `arguments`
// as a JSON-encoded STRING (OpenAI/OpenRouter reject objects), and tool
// results must reference the call id. The mock provider masked this bug.
func TestOpenAICompatToolCallRoundTrip(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.chatBody = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
	p := oaiProvider(t, s, "")
	call := core.ToolCall{ID: "call_9", Name: "calculator", Arguments: []byte(`{"expression":"6*7"}`)}
	_, err := p.Chat(context.Background(), ChatRequest{
		Model: "test-model",
		Messages: []core.Message{
			core.NewUserMessage("6*7?"),
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{call}},
			{Role: core.RoleTool, ToolCallID: "call_9", Content: `{"result":42}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := s.lastReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	tc := msgs[1].ToolCalls
	if len(tc) != 1 {
		t.Fatalf("tool calls = %+v", tc)
	}
	// The critical assertion: arguments must be the raw JSON as a string.
	if tc[0].Function.Arguments != `{"expression":"6*7"}` {
		t.Fatalf("arguments = %q, want JSON-encoded string", tc[0].Function.Arguments)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_9" {
		t.Fatalf("tool result message wrong: %+v", msgs[2])
	}
}

func TestOpenAICompatChatError(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.status = 401
	s.chatBody = `{"error": {"message": "bad key"}}`
	p := oaiProvider(t, s, "")
	_, err := p.Chat(context.Background(), ChatRequest{})
	ee, ok := err.(*core.Error)
	if !ok {
		t.Fatalf("expected typed error, got %v", err)
	}
	if ee.Kind != core.KindProvider {
		t.Fatalf("kind = %s", ee.Kind)
	}
	if !core.Retryable(err) {
		t.Fatal("provider HTTP error must be retryable")
	}
}

func TestOpenAICompatStream(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.streamBody = "data: " + `{"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"content":"lo"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"calculator","arguments":"{\"expression\":\""}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1+1\"}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: " + `{"usage":{"prompt_tokens":3,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
	p := oaiProvider(t, s, "")
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
	if content != "Hello" {
		t.Fatalf("streamed content = %q", content)
	}
	// Tool-call deltas must be accumulated into one complete call.
	if len(calls) != 1 {
		t.Fatalf("expected 1 accumulated tool call, got %+v", calls)
	}
	if calls[0].ID != "call_9" || calls[0].Name != "calculator" {
		t.Fatalf("accumulated call wrong: %+v", calls[0])
	}
	if string(calls[0].Arguments) != `{"expression":"1+1"}` {
		t.Fatalf("accumulated args wrong: %s", calls[0].Arguments)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish = %q", finish)
	}
	if usage == nil || usage.InputTokens != 3 || usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", usage)
	}
	if s.lastReq.Stream != true {
		t.Fatal("expected stream=true in request body")
	}
}

func TestOpenAICompatEmbed(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.embedBody = `{"data":[{"embedding":[1.0,2.5,3.0]},{"embedding":[4.0,5.0,6.0]}]}`
	p := oaiProvider(t, s, "")
	vecs, err := p.(Embedder).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 || vecs[0][1] != 2.5 {
		t.Fatalf("vectors wrong: %+v", vecs)
	}
	if !strings.HasSuffix(s.embedPath, "/embeddings") {
		t.Fatalf("embed path = %s", s.embedPath)
	}
}

func TestOllamaEmbedStyle(t *testing.T) {
	s := newOAITestServer(t)
	defer s.close()
	s.embedBody = `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`
	p := oaiProvider(t, s, "ollama")
	vecs, err := p.(Embedder).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 2 || vecs[1][1] != 0.4 {
		t.Fatalf("ollama vectors wrong: %+v", vecs)
	}
	if s.embedPath != "/api/embed" {
		t.Fatalf("ollama embed path = %s", s.embedPath)
	}
}

func TestOpenAICompatProviderFactories(t *testing.T) {
	if OpenAI("k", "gpt-4o").Model() != "gpt-4o" {
		t.Fatal("OpenAI factory wrong")
	}
	if Groq("k", "m").Model() != "m" {
		t.Fatal("Groq factory wrong")
	}
	if Mistral("k", "m").Model() != "m" {
		t.Fatal("Mistral factory wrong")
	}
	if OpenRouter("k", "m").Model() != "m" {
		t.Fatal("OpenRouter factory wrong")
	}
	if Ollama("", "qwen").Model() != "qwen" {
		t.Fatal("Ollama factory wrong")
	}
}
