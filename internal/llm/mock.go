package llm

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"sync"
	"time"

	"ernest/internal/core"
)

// MockTurn is one scripted model response.
type MockTurn struct {
	Content      string
	ToolCalls    []core.ToolCall
	FinishReason string
	Usage        *core.Usage
}

// MockConfig configures the deterministic test provider.
type MockConfig struct {
	ID        string
	Model     string
	Script    []MockTurn // responses, one per model call
	Default   MockTurn   // used when the script is exhausted
	Stream    bool       // emit content as 1-char deltas in Stream()
	Delay     time.Duration // per-chunk delay in Stream() (interrupt tests)
	EmbedDim  int        // >0 enables Embed with deterministic vectors
	EmbedErr  error
}

// MockProvider is a fully deterministic provider used by tests and the
// playground's "mock" provider option. It never touches the network.
type MockProvider struct {
	cfg MockConfig
	mu  sync.Mutex
	calls int

	// Requests records every ChatRequest for assertions.
	Requests []ChatRequest
}

// NewMock builds a scripted provider.
func NewMock(cfg MockConfig) *MockProvider {
	if cfg.ID == "" {
		cfg.ID = "mock"
	}
	if cfg.Model == "" {
		cfg.Model = "mock-1"
	}
	// Without a script or a default turn the mock would answer with an
	// empty reply. Give it a friendly canned response so playgrounds,
	// templates and demos show output out of the box.
	if len(cfg.Script) == 0 && cfg.Default.Content == "" && len(cfg.Default.ToolCalls) == 0 {
		cfg.Default = MockTurn{
			Content:      "Hello from the mock provider. Swap me for a real model in ernest.json!",
			FinishReason: "stop",
			Usage:        &core.Usage{InputTokens: 24, OutputTokens: 42},
		}
	}
	return &MockProvider{cfg: cfg}
}

func (m *MockProvider) ID() string    { return m.cfg.ID }
func (m *MockProvider) Model() string { return m.cfg.Model }

// CallCount reports how many model calls have been made.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Scripted returns the configured script (for assertions).
func (m *MockProvider) Script() []MockTurn { return m.cfg.Script }

func (m *MockProvider) nextTurn() MockTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
	var turn MockTurn
	if m.calls < len(m.cfg.Script) {
		turn = m.cfg.Script[m.calls]
	} else {
		turn = m.cfg.Default
	}
	m.calls++
	return turn
}

func (m *MockProvider) record(req ChatRequest) {
	m.mu.Lock()
	m.Requests = append(m.Requests, req)
	m.mu.Unlock()
}

// Chat returns the scripted response.
func (m *MockProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	m.record(req)
	t := m.nextTurn()
	return ChatResponse{Content: t.Content, ToolCalls: t.ToolCalls, FinishReason: t.FinishReason, Usage: t.Usage}, nil
}

// Stream emits the scripted response, optionally as character deltas.
// It honours ctx cancellation mid-stream: on cancel the done channel is
// closed and the stream ends with context.Canceled (interrupts surface
// as errors, never as silent truncation).
func (m *MockProvider) Stream(ctx context.Context, req ChatRequest) (Stream, error) {
	m.record(req)
	t := m.nextTurn()
	ch := make(chan StreamChunk, 256)
	done := make(chan struct{})
	go func() {
		defer func() {
			select {
			case <-ctx.Done():
				close(done)
			default:
			}
		}()
		send := func(c StreamChunk) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- c:
				return true
			}
		}
		if m.cfg.Stream {
			for _, r := range t.Content {
				if !send(StreamChunk{Content: string(r)}) {
					return
				}
				if m.cfg.Delay > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(m.cfg.Delay):
					}
				}
			}
		} else if t.Content != "" {
			if !send(StreamChunk{Content: t.Content}) {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case ch <- StreamChunk{ToolCalls: t.ToolCalls, FinishReason: t.FinishReason, Usage: t.Usage}:
		}
		close(ch)
	}()
	return &channelStream{ch: ch, ctx: ctx, done: done}, nil
}

// Embed returns deterministic hash-based vectors of EmbedDim.
func (m *MockProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if m.cfg.EmbedErr != nil {
		return nil, m.cfg.EmbedErr
	}
	dim := m.cfg.EmbedDim
	if dim <= 0 {
		dim = 8
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		h := fnv.New32a()
		_, _ = h.Write([]byte(t))
		seed := int64(h.Sum32())
		v := make([]float32, dim)
		for i := range v {
			seed = seed*6364136223846793005 + 1442695040888963407
			v[i] = float32(seed%1000) / 1000.0
		}
		out = append(out, v)
	}
	return out, nil
}

// MockStreamJSON serialises a slice of StreamChunks (helper for tests).
func MockStreamJSON(chunks []StreamChunk) []byte {
	data, _ := json.Marshal(chunks)
	return data
}
