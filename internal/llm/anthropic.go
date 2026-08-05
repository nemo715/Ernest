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

	"ernest/internal/core"
)

// AnthropicConfig configures the Anthropic provider.
type AnthropicConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

type anthropic struct {
	cfg  AnthropicConfig
	http *http.Client
}

// Anthropic is the Anthropic provider (e.g. "claude-sonnet-4-20250514").
func Anthropic(apiKey, model string) Provider {
	return NewAnthropic(AnthropicConfig{APIKey: apiKey, Model: model})
}

// NewAnthropic builds an Anthropic provider with full configuration.
func NewAnthropic(cfg AnthropicConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &anthropic{cfg: cfg, http: cfg.HTTPClient}
}

func (p *anthropic) ID() string    { return "anthropic" }
func (p *anthropic) Model() string { return p.cfg.Model }

type anthropicContentBlock struct {
	Type  string `json:"type"` // text | tool_use | tool_result
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// tool_result fields:
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string                 `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	System        string                 `json:"system,omitempty"`
	Messages      []anthropicMessage     `json:"messages"`
	Tools         []anthropicTool        `json:"tools,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func toAnthropicMessages(messages []core.Message) ([]anthropicMessage, string) {
	var system []string
	var out []anthropicMessage
	for _, m := range messages {
		switch m.Role {
		case core.RoleSystem:
			system = append(system, m.Text())
		case core.RoleUser:
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{{Type: "text", Text: m.Text()}}})
		case core.RoleAssistant:
			blocks := []anthropicContentBlock{}
			if text := m.Text(); text != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: text})
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal(tc.Arguments, &input)
				blocks = append(blocks, anthropicContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		case core.RoleTool:
			block := anthropicContentBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content}
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{block}})
		}
	}
	return out, strings.Join(system, "\n")
}

func (p *anthropic) buildRequest(ctx context.Context, req ChatRequest, stream bool) (*http.Request, error) {
	messages, system := toAnthropicMessages(req.Messages)
	mt := req.MaxTokens
	if mt == 0 {
		mt = 4096
	}
	body := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     mt,
		System:        system,
		Messages:      messages,
		Temperature:   req.Temperature,
		StopSequences: req.Stop,
		Stream:        stream,
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	return httpReq, nil
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (u *anthropicUsage) toCore() *core.Usage {
	if u == nil {
		return nil
	}
	return &core.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

func (p *anthropic) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	httpReq, err := p.buildRequest(ctx, req, false)
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "anthropic request failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResponse{}, core.NewError(core.KindProvider, fmt.Sprintf("anthropic HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	var out struct {
		Content    []anthropicContentBlock `json:"content"`
		Usage      *anthropicUsage         `json:"usage"`
		StopReason string                  `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "anthropic decode: "+err.Error(), err)
	}
	var cr ChatResponse
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			cr.Content += b.Text
		case "tool_use":
			args, _ := json.Marshal(b.Input)
			cr.ToolCalls = append(cr.ToolCalls, core.ToolCall{ID: b.ID, Name: b.Name, Arguments: args})
		}
	}
	cr.FinishReason = out.StopReason
	cr.Usage = out.Usage.toCore()
	return cr, nil
}

func (p *anthropic) Stream(ctx context.Context, req ChatRequest) (Stream, error) {
	httpReq, err := p.buildRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, core.NewError(core.KindProvider, "anthropic stream request failed: "+err.Error(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, core.NewError(core.KindProvider, fmt.Sprintf("anthropic HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
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
		// Tool-use blocks stream their JSON input as string deltas.
		type toolAcc struct {
			id   string
			name string
			args []byte
		}
		acc := map[string]*toolAcc{}
		var usage *core.Usage
		stop := ""
		_ = ParseSSE(pr, func(ev SSEEvent) error {
			if ev.Event == "" {
				return nil // ping / keepalive
			}
			var d map[string]any
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				return err
			}
			switch ev.Event {
			case "content_block_delta":
				delta, _ := d["delta"].(map[string]any)
				if delta == nil {
					return nil
				}
				if t, ok := delta["type"].(string); ok {
					switch t {
					case "text_delta":
						if txt, ok := delta["text"].(string); ok {
							ch <- StreamChunk{Content: txt}
						}
					case "input_json_delta":
						idx := int(d["index"].(float64))
						if partial, ok := delta["partial_json"].(string); ok {
							key := fmt.Sprintf("%d", idx)
							a := acc[key]
							if a == nil {
								a = &toolAcc{}
								acc[key] = a
							}
							a.args = append(a.args, partial...)
						}
					}
				}
			case "content_block_start":
				if cb, ok := d["content_block"].(map[string]any); ok {
					if t, ok := cb["type"].(string); ok && t == "tool_use" {
						idx := int(d["index"].(float64))
						key := fmt.Sprintf("%d", idx)
						a := &toolAcc{id: fmt.Sprint(cb["id"]), name: fmt.Sprint(cb["name"])}
						acc[key] = a
					}
				}
			case "message_delta":
				if du, ok := d["usage"].(map[string]any); ok {
					usage = &core.Usage{
						InputTokens:  int(du["input_tokens"].(float64)),
						OutputTokens: int(du["output_tokens"].(float64)),
					}
				}
				if dd, ok := d["delta"].(map[string]any); ok {
					if sr, ok := dd["stop_reason"].(string); ok {
						stop = sr
					}
				}
			}
			return nil
		})
		var calls []core.ToolCall
		for _, a := range acc {
			calls = append(calls, core.ToolCall{ID: a.id, Name: a.name, Arguments: json.RawMessage(a.args)})
		}
		ch <- StreamChunk{ToolCalls: calls, FinishReason: stop, Usage: usage}
	}()
	return &channelStream{ch: ch, ctx: ctx, done: make(chan struct{})}, nil
}
