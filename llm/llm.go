// Package llm is the public API for language-model providers.
// It forwards to the implementation in ernest/internal/llm.
package llm

import (
	"context"

	internal "github.com/nemo715/Ernest/internal/llm"
)

type (
	Provider         = internal.Provider
	MockConfig       = internal.MockConfig
	MockTurn         = internal.MockTurn
	MockProvider     = internal.MockProvider
	ChatRequest      = internal.ChatRequest
	ChatResponse     = internal.ChatResponse
	Stream           = internal.Stream
	StreamChunk      = internal.StreamChunk
	OpenAICompatConfig = internal.OpenAICompatConfig
)

// NewMock builds a scripted, deterministic provider (no network).
func NewMock(cfg MockConfig) *MockProvider {
	return internal.NewMock(cfg)
}

// NewOpenAICompat builds a provider for any OpenAI-compatible endpoint
// (OpenRouter, Groq, Ollama, vLLM, …) via BaseURL.
func NewOpenAICompat(cfg OpenAICompatConfig) Provider {
	return internal.NewOpenAICompat(cfg)
}

// Chat is a convenience wrapper for a synchronous call.
func Chat(ctx context.Context, p Provider, req ChatRequest) (ChatResponse, error) {
	return p.Chat(ctx, req)
}
