// Package llm defines the provider abstraction and the built-in
// providers: OpenAI, Anthropic, Gemini, and any OpenAI-compatible API
// (Groq, Mistral, Ollama, OpenRouter, local servers).
package llm

import (
	"context"
	"errors"
	"io"

	"github.com/nemo715/Ernest/internal/core"
)

// ChatRequest is a single model call.
type ChatRequest struct {
	Model        string
	Messages     []core.Message
	Tools        []*core.Tool
	Temperature  *float64
	MaxTokens    int
	Stop         []string
	ResponseSchema *core.Schema // structured output (JSON mode)
	ResponseSchemaName string
}

// ChatResponse is a non-streamed model response.
type ChatResponse struct {
	Content      string
	ToolCalls    []core.ToolCall
	FinishReason string
	Usage        *core.Usage
}

// StreamChunk is one piece of a streaming response. Tool calls are only
// emitted complete (accumulated by the provider) on the final chunk.
type StreamChunk struct {
	Content      string
	ToolCalls    []core.ToolCall
	FinishReason string
	Usage        *core.Usage
}

// ErrStreamDone is returned by Stream.Next when the stream is exhausted.
var ErrStreamDone = errors.New("stream done")

// Stream yields chunks until ErrStreamDone.
type Stream interface {
	Next() (StreamChunk, error)
	Close() error
}

// Embedder is implemented by providers that support embeddings
// (used by KnowledgeBase).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Provider is the model abstraction. Providers must be safe for
// concurrent use.
type Provider interface {
	ID() string
	Model() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest) (Stream, error)
}

// EmbedIfSupported returns embeddings when the provider supports them.
func EmbedIfSupported(p Provider, ctx context.Context, texts []string) ([][]float32, bool, error) {
	em, ok := p.(Embedder)
	if !ok {
		return nil, false, nil
	}
	vecs, err := em.Embed(ctx, texts)
	return vecs, true, err
}

// channelStream adapts a channel of chunks into a Stream. When ctx is
// non-nil and gets cancelled, Next returns the cancellation error so
// interrupted generations surface as errors (KindInterrupt) instead of
// silently truncating.
type channelStream struct {
	ch    <-chan StreamChunk
	ctx   context.Context
	done  chan struct{}
	close func()
	err   error
}

func (s *channelStream) Next() (StreamChunk, error) {
	// Cancellation wins deterministically: interrupted generations must
	// surface as ctx errors, never as a silent EOF mid-generation.
	if s.ctx != nil {
		select {
		case <-s.ctx.Done():
			return StreamChunk{}, s.ctx.Err()
		default:
		}
	}
	select {
	case c, ok := <-s.ch:
		if !ok {
			if s.ctx != nil && s.ctx.Err() != nil {
				return StreamChunk{}, s.ctx.Err()
			}
			if s.err != nil {
				return StreamChunk{}, s.err
			}
			return StreamChunk{}, io.EOF
		}
		return c, nil
	case <-s.done:
		if s.ctx != nil && s.ctx.Err() != nil {
			return StreamChunk{}, s.ctx.Err()
		}
		return StreamChunk{}, io.EOF
	}
}

func (s *channelStream) Close() error {
	if s.close != nil {
		s.close()
	}
	return nil
}
