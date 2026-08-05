package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ernest/internal/core"
)

// GeminiConfig configures the Google Gemini provider.
type GeminiConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

type gemini struct {
	cfg  GeminiConfig
	http *http.Client
}

// Gemini is the Google Gemini provider (e.g. "gemini-2.0-flash").
func Gemini(apiKey, model string) Provider {
	return NewGemini(GeminiConfig{APIKey: apiKey, Model: model})
}

// NewGemini builds a Gemini provider with full configuration.
func NewGemini(cfg GeminiConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &gemini{cfg: cfg, http: cfg.HTTPClient}
}

func (p *gemini) ID() string    { return "gemini" }
func (p *gemini) Model() string { return p.cfg.Model }

type geminiPart struct {
	Text              string               `json:"text,omitempty"`
	FunctionCall      *geminiFunctionCall  `json:"functionCall,omitempty"`
	FunctionResponse  *geminiFunctionResp  `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // user | model
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiRequest struct {
	Contents         []geminiContent      `json:"contents"`
	SystemInstruction *geminiContent      `json:"systemInstruction,omitempty"`
	Tools            []map[string]any     `json:"tools,omitempty"`
	GenerationConfig map[string]any       `json:"generationConfig,omitempty"`
}

func toGeminiContents(messages []core.Message) []geminiContent {
	var out []geminiContent
	for _, m := range messages {
		switch m.Role {
		case core.RoleUser:
			out = append(out, geminiContent{Role: "user", Parts: []geminiPart{{Text: m.Text()}}})
		case core.RoleAssistant:
			parts := []geminiPart{}
			if text := m.Text(); text != "" {
				parts = append(parts, geminiPart{Text: text})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: tc.Arguments}})
			}
			out = append(out, geminiContent{Role: "model", Parts: parts})
		case core.RoleTool:
			out = append(out, geminiContent{Role: "user", Parts: []geminiPart{{
				FunctionResponse: &geminiFunctionResp{Name: m.Name, Response: map[string]any{"result": json.RawMessage(m.Content)}},
			}}})
		case core.RoleSystem:
			// handled via systemInstruction below
		}
	}
	return out
}

func (p *gemini) buildRequest(ctx context.Context, req ChatRequest, stream bool) (*http.Request, error) {
	contents := toGeminiContents(req.Messages)
	var systemText []string
	for _, m := range req.Messages {
		if m.Role == core.RoleSystem {
			systemText = append(systemText, m.Text())
		}
	}
	body := geminiRequest{Contents: contents}
	if len(systemText) > 0 {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: strings.Join(systemText, "\n")}}}
	}
	if len(req.Tools) > 0 {
		var decls []geminiFunctionDecl
		for _, t := range req.Tools {
			decls = append(decls, geminiFunctionDecl{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		}
		body.Tools = []map[string]any{{"functionDeclarations": decls}}
	}
	gc := map[string]any{}
	if req.Temperature != nil {
		gc["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		gc["maxOutputTokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		gc["stopSequences"] = req.Stop
	}
	if req.ResponseSchema != nil {
		schemaJSON, err := req.ResponseSchema.SchemaJSON()
		if err != nil {
			return nil, err
		}
		var schemaObj map[string]any
		if err := json.Unmarshal(schemaJSON, &schemaObj); err != nil {
			return nil, err
		}
		gc["responseMimeType"] = "application/json"
		gc["responseSchema"] = schemaObj
	}
	if len(gc) > 0 {
		body.GenerationConfig = gc
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := "generateContent"
	if stream {
		endpoint = "streamGenerateContent"
	}
	u := fmt.Sprintf("%s/models/%s:%s", p.cfg.BaseURL, url.PathEscape(req.Model), endpoint)
	if stream {
		u += "?alt=sse"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	q := httpReq.URL.Query()
	q.Set("key", p.cfg.APIKey)
	httpReq.URL.RawQuery = q.Encode()
	return httpReq, nil
}

func (p *gemini) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	httpReq, err := p.buildRequest(ctx, req, false)
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "gemini request failed: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResponse{}, core.NewError(core.KindProvider, fmt.Sprintf("gemini HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	var out struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChatResponse{}, core.NewError(core.KindProvider, "gemini decode: "+err.Error(), err)
	}
	var cr ChatResponse
	if len(out.Candidates) > 0 {
		for _, part := range out.Candidates[0].Content.Parts {
			if part.Text != "" {
				cr.Content += part.Text
			}
			if part.FunctionCall != nil {
				cr.ToolCalls = append(cr.ToolCalls, core.ToolCall{ID: genCallID(cr.ToolCalls, part.FunctionCall.Name), Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args})
			}
		}
		cr.FinishReason = out.Candidates[0].FinishReason
	}
	if out.UsageMetadata != nil {
		cr.Usage = &core.Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount}
	}
	return cr, nil
}

func (p *gemini) Stream(ctx context.Context, req ChatRequest) (Stream, error) {
	httpReq, err := p.buildRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, core.NewError(core.KindProvider, "gemini stream request failed: "+err.Error(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, core.NewError(core.KindProvider, fmt.Sprintf("gemini HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
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
		var calls []core.ToolCall
		finish := ""
		var usage *core.Usage
		_ = ParseSSE(pr, func(ev SSEEvent) error {
			return SSEEventSource(ev.Data, func(data []byte) error {
				var d struct {
					Candidates []struct {
						Content      geminiContent `json:"content"`
						FinishReason string        `json:"finishReason"`
					} `json:"candidates"`
					UsageMetadata *struct {
						PromptTokenCount     int `json:"promptTokenCount"`
						CandidatesTokenCount int `json:"candidatesTokenCount"`
					} `json:"usageMetadata"`
				}
				if err := json.Unmarshal(data, &d); err != nil {
					return err
				}
				if len(d.Candidates) > 0 {
					for _, part := range d.Candidates[0].Content.Parts {
						if part.Text != "" {
							ch <- StreamChunk{Content: part.Text}
						}
						if part.FunctionCall != nil {
							calls = append(calls, core.ToolCall{ID: genCallID(calls, part.FunctionCall.Name), Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args})
						}
					}
					if d.Candidates[0].FinishReason != "" {
						finish = d.Candidates[0].FinishReason
					}
				}
				if d.UsageMetadata != nil {
					usage = &core.Usage{InputTokens: d.UsageMetadata.PromptTokenCount, OutputTokens: d.UsageMetadata.CandidatesTokenCount}
				}
				return nil
			})
		})
		ch <- StreamChunk{ToolCalls: calls, FinishReason: finish, Usage: usage}
	}()
	return &channelStream{ch: ch, ctx: ctx, done: make(chan struct{})}, nil
}

func genCallID(calls []core.ToolCall, name string) string {
	n := 0
	for _, c := range calls {
		if c.Name == name {
			n++
		}
	}
	return fmt.Sprintf("call_%s_%d", name, n)
}
