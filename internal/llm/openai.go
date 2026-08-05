package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

// OpenAICompatConfig configures any OpenAI-compatible chat endpoint
// (OpenAI, Groq, Mistral, OpenRouter, Ollama /v1, local servers).
type OpenAICompatConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	// EmbedStyle selects the embeddings payload shape:
	// "openai" (POST {base}/embeddings) or "ollama" (POST {base}/api/embed).
	EmbedStyle string
	EmbedModel string
	// ExtraHeaders are merged into every request (e.g. x-openrouter-*).
	ExtraHeaders map[string]string
	HTTPClient   *http.Client
}

type openAICompat struct {
	id   string
	cfg  OpenAICompatConfig
	http *http.Client
}

// NewOpenAICompat builds a provider for any OpenAI-compatible endpoint.
func NewOpenAICompat(cfg OpenAICompatConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &openAICompat{id: "openai_compatible", cfg: cfg, http: cfg.HTTPClient}
}

// OpenAI is the OpenAI provider (model e.g. "gpt-4o").
func OpenAI(apiKey, model string) Provider {
	return NewOpenAICompat(OpenAICompatConfig{BaseURL: "https://api.openai.com/v1", APIKey: apiKey, Model: model, EmbedModel: model})
}

// Groq is the Groq provider (e.g. "llama-3.3-70b-versatile").
func Groq(apiKey, model string) Provider {
	return NewOpenAICompat(OpenAICompatConfig{BaseURL: "https://api.groq.com/openai/v1", APIKey: apiKey, Model: model})
}

// Mistral is the Mistral provider (e.g. "mistral-large-latest").
func Mistral(apiKey, model string) Provider {
	return NewOpenAICompat(OpenAICompatConfig{BaseURL: "https://api.mistral.ai/v1", APIKey: apiKey, Model: model})
}

// OpenRouter is the OpenRouter provider (many models, one key).
func OpenRouter(apiKey, model string) Provider {
	return NewOpenAICompat(OpenAICompatConfig{BaseURL: "https://openrouter.ai/api/v1", APIKey: apiKey, Model: model})
}

// Ollama is the local Ollama provider. baseURL defaults to
// http://localhost:11434 (chat uses the /v1 OpenAI-compatible endpoint,
// embeddings use /api/embed).
func Ollama(baseURL, model string) Provider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return NewOpenAICompat(OpenAICompatConfig{
		BaseURL:    baseURL + "/v1",
		Model:      model,
		EmbedStyle: "ollama",
		EmbedModel: model,
	})
}

func (p *openAICompat) ID() string  { return p.id }
func (p *openAICompat) Model() string { return p.cfg.Model }

// ---------------------------------------------------------------------------

type oaiMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content,omitempty"`
	ToolCalls  []oaiToolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type oaiToolCallMsg struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

// oaiFunction matches the OpenAI wire format: tool call arguments are a
// JSON-encoded STRING, not an object.
type oaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiToolDef struct {
	Type     string         `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiChatRequest struct {
	Model          string         `json:"model"`
	Messages       []oaiMessage   `json:"messages"`
	Tools          []oaiToolDef   `json:"tools,omitempty"`
	Temperature    *float64       `json:"temperature,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Stop           []string       `json:"stop,omitempty"`
	Stream         bool           `json:"stream"`
	StreamOptions  *oaiStreamOpts `json:"stream_options,omitempty"`
	ResponseFormat *oaiRespFormat `json:"response_format,omitempty"`
}

type oaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiRespFormat struct {
	Type       string           `json:"type"`
	JSONSchema *oaiJSONSchema   `json:"json_schema,omitempty"`
}

type oaiJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

func toOAIMessages(messages []core.Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(messages))
	for _, m := range messages {
		om := oaiMessage{Role: string(m.Role), Name: m.Name}
		content := m.Text()
		switch m.Role {
		case core.RoleUser, core.RoleSystem:
			om.Content = &content
		case core.RoleAssistant:
			om.Content = &content
			if len(m.ToolCalls) > 0 {
				tcs := make([]oaiToolCallMsg, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					tcs = append(tcs, oaiToolCallMsg{
						ID:   tc.ID,
						Type: "function",
						Function: oaiFunction{Name: tc.Name, Arguments: string(tc.Arguments)},
					})
				}
				om.ToolCalls = tcs
			}
		case core.RoleTool:
			om.Content = &content
			om.ToolCallID = toolCallIDFromMessage(m)
		}
		out = append(out, om)
	}
	return out
}

// toolCallIDFromMessage recovers the tool call id being answered.
func toolCallIDFromMessage(m core.Message) string {
	return m.ToolCallID
}

// oaiUsage matches the OpenAI wire format for token usage.
type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u *oaiUsage) toCore() *core.Usage {
	if u == nil {
		return nil
	}
	return &core.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

func (p *openAICompat) buildRequest(ctx context.Context, req ChatRequest, stream bool) (*http.Request, error) {
	body := oaiChatRequest{
		Model:       req.Model,
		Messages:    toOAIMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
		Stream:      stream,
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, oaiToolDef{
			Type: "function",
			Function: oaiToolFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	if stream {
		body.StreamOptions = &oaiStreamOpts{IncludeUsage: true}
	}
	if req.ResponseSchema != nil {
		schemaJSON, err := req.ResponseSchema.SchemaJSON()
		if err != nil {
			return nil, err
		}
		name := req.ResponseSchemaName
		if name == "" {
			name = "output"
		}
		body.ResponseFormat = &oaiRespFormat{Type: "json_schema", JSONSchema: &oaiJSONSchema{Name: name, Schema: schemaJSON, Strict: false}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := p.cfg.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	for k, v := range p.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

func (p *openAICompat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	httpReq, err := p.buildRequest(ctx, req, false)
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "openai request failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResponse{}, core.NewError(core.KindProvider, fmt.Sprintf("openai HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	var out struct {
		Choices []struct {
			Message      oaiMessage `json:"message"`
			FinishReason string     `json:"finish_reason"`
		} `json:"choices"`
		Usage *oaiUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "openai decode: "+err.Error(), err)
	}
	var cr ChatResponse
	if len(out.Choices) > 0 {
		c := out.Choices[0]
		if c.Message.Content != nil {
			cr.Content = *c.Message.Content
		}
		for _, tc := range c.Message.ToolCalls {
			cr.ToolCalls = append(cr.ToolCalls, core.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: []byte(tc.Function.Arguments)})
		}
		cr.FinishReason = c.FinishReason
	}
	cr.Usage = out.Usage.toCore()
	return cr, nil
}

func (p *openAICompat) Stream(ctx context.Context, req ChatRequest) (Stream, error) {
	httpReq, err := p.buildRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, core.NewError(core.KindProvider, "openai stream request failed: "+err.Error(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, core.NewError(core.KindProvider, fmt.Sprintf("openai HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	ch := make(chan StreamChunk, 64)
	pr, pw := io.Pipe()
	go func() {
		defer resp.Body.Close()
		_, _ = io.Copy(pw, resp.Body)
		pw.Close()
	}()
	go func() {
		defer close(ch)
		// Accumulate tool-call deltas (OpenAI streams name/arguments in
		// pieces indexed by `index`); emit complete calls in the final chunk.
		type tcAcc struct {
			id   string
			name string
			args []byte
		}
		acc := map[int]*tcAcc{}
		finish := ""
		var usage *core.Usage
		err := ParseSSE(pr, func(ev SSEEvent) error {
			return SSEEventSource(ev.Data, func(data []byte) error {
				var d struct {
					Choices []struct {
						Delta struct {
							Content   *string `json:"content"`
							ToolCalls []struct {
								Index    int    `json:"index"`
								ID       string `json:"id"`
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
					Usage *oaiUsage `json:"usage"`
				}
				if err := json.Unmarshal(data, &d); err != nil {
					return err
				}
				if len(d.Choices) > 0 {
					c := d.Choices[0]
					if c.Delta.Content != nil && *c.Delta.Content != "" {
						ch <- StreamChunk{Content: *c.Delta.Content}
					}
					for _, tc := range c.Delta.ToolCalls {
						a := acc[tc.Index]
						if a == nil {
							a = &tcAcc{}
							acc[tc.Index] = a
						}
						if tc.ID != "" {
							a.id = tc.ID
						}
						if tc.Function.Name != "" {
							a.name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							a.args = append(a.args, tc.Function.Arguments...)
						}
					}
					if c.FinishReason != "" {
						finish = c.FinishReason
					}
				}
				if d.Usage != nil {
					usage = d.Usage.toCore()
				}
				return nil
			})
		})
		// Emit accumulated tool calls in the final chunk.
		var calls []core.ToolCall
		for _, a := range acc {
			calls = append(calls, core.ToolCall{ID: a.id, Name: a.name, Arguments: json.RawMessage(a.args)})
		}
		ch <- StreamChunk{ToolCalls: calls, FinishReason: finish, Usage: usage}
		if err != nil {
			_ = err // stream errors surface as EOF; the run loop retries
		}
	}()
	return &channelStream{ch: ch, ctx: ctx, done: make(chan struct{})}, nil
}

// Embed implements the OpenAI-compatible embeddings API.
func (p *openAICompat) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	model := p.cfg.EmbedModel
	if model == "" {
		model = p.cfg.Model
	}
	if p.cfg.EmbedStyle == "ollama" {
		return p.embedOllama(ctx, model, texts)
	}
	payload := map[string]any{"model": model, "input": texts}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := p.cfg.BaseURL + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, core.NewError(core.KindProvider, "embedding request failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, core.NewError(core.KindProvider, fmt.Sprintf("embeddings HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, core.NewError(core.KindProvider, "embeddings decode: "+err.Error(), err)
	}
	vecs := make([][]float32, 0, len(out.Data))
	for _, d := range out.Data {
		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}
		vecs = append(vecs, v)
	}
	return vecs, nil
}

func (p *openAICompat) embedOllama(ctx context.Context, model string, texts []string) ([][]float32, error) {
	payload := map[string]any{"model": model, "input": texts}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(p.cfg.BaseURL, "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, core.NewError(core.KindProvider, "ollama embed failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, core.NewError(core.KindProvider, fmt.Sprintf("ollama embed HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	var out struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, core.NewError(core.KindProvider, "ollama embed decode: "+err.Error(), err)
	}
	vecs := make([][]float32, 0, len(out.Embeddings))
	for _, e := range out.Embeddings {
		v := make([]float32, len(e))
		for i, f := range e {
			v[i] = float32(f)
		}
		vecs = append(vecs, v)
	}
	return vecs, nil
}
