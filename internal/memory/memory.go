// Package memory provides conversation memory: session persistence and
// history-trimming strategies (full, sliding window, token budget).
package memory

import (
	"context"
	"encoding/json"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/storage"
)

// Strategy trims conversation history before it is sent to the model.
type Strategy interface {
	// Trim returns the messages that should be sent.
	Trim(messages []core.Message) []core.Message
}

// FullStrategy keeps the entire history.
type FullStrategy struct{}

func (FullStrategy) Trim(messages []core.Message) []core.Message { return messages }

// SlidingWindowStrategy keeps the last N messages (plus the system
// prefix, which is added by the agent and not part of history).
type SlidingWindowStrategy struct {
	MaxMessages int
}

func (s SlidingWindowStrategy) Trim(messages []core.Message) []core.Message {
	if s.MaxMessages <= 0 || len(messages) <= s.MaxMessages {
		return messages
	}
	return messages[len(messages)-s.MaxMessages:]
}

// TokenBudgetStrategy approximates a token budget by character count
// (roughly 4 chars per token), trimming from the oldest message.
type TokenBudgetStrategy struct {
	MaxChars int
}

func (s TokenBudgetStrategy) Trim(messages []core.Message) []core.Message {
	if s.MaxChars <= 0 {
		return messages
	}
	total := 0
	for i := len(messages) - 1; i >= 0; i-- {
		total += len(messages[i].Text()) + 32 // message overhead
		if total > s.MaxChars && i > 0 {
			return messages[i:]
		}
	}
	return messages
}

// Memory binds a session to a store and a strategy. It is the default
// agent memory: durable history with automatic trimming.
type Memory struct {
	Store     storage.SessionStore
	SessionID string
	UserID    string
	Strategy  Strategy
}

// New builds a Memory over a store.
func New(store storage.SessionStore, opts ...Option) *Memory {
	m := &Memory{Store: store, Strategy: FullStrategy{}}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Option configures a Memory.
type Option func(*Memory)

// WithSessionID sets the session key.
func WithSessionID(id string) Option { return func(m *Memory) { m.SessionID = id } }

// WithUserID sets the owning user.
func WithUserID(id string) Option { return func(m *Memory) { m.UserID = id } }

// WithStrategy sets the trimming strategy.
func WithStrategy(s Strategy) Option { return func(m *Memory) { m.Strategy = s } }

// Load returns the session, creating it when missing.
func (m *Memory) Load(ctx context.Context) (*storage.Session, error) {
	if m.Store == nil {
		return nil, core.NewError(core.KindMemory, "memory has no store")
	}
	if m.SessionID == "" {
		return nil, core.NewError(core.KindMemory, "memory has no session id")
	}
	sess, err := m.Store.Get(ctx, m.SessionID)
	if err != nil {
		if core.KindOf(err) == core.KindMemory && contains(err.Error(), "not found") {
			sess = storage.NewSession(m.SessionID, "", m.UserID)
			if err := m.Store.Save(ctx, sess); err != nil {
				return nil, err
			}
			return sess, nil
		}
		return nil, err
	}
	return sess, nil
}

// History returns the trimmed conversation history.
func (m *Memory) History(ctx context.Context) ([]core.Message, error) {
	sess, err := m.Load(ctx)
	if err != nil {
		return nil, err
	}
	return m.Strategy.Trim(sess.Messages), nil
}

// Append adds a message and persists the session.
func (m *Memory) Append(ctx context.Context, msg core.Message) error {
	sess, err := m.Load(ctx)
	if err != nil {
		return err
	}
	sess.Messages = append(sess.Messages, msg)
	return m.Store.Save(ctx, sess)
}

// Save persists the full session (used by the agent run loop for
// checkpoints and HITL state).
func (m *Memory) Save(ctx context.Context, sess *storage.Session) error {
	if m.Store == nil {
		return core.NewError(core.KindMemory, "memory has no store")
	}
	return m.Store.Save(ctx, sess)
}

// Clear wipes the session history.
func (m *Memory) Clear(ctx context.Context) error {
	sess, err := m.Load(ctx)
	if err != nil {
		return err
	}
	sess.Messages = nil
	sess.PendingApprovals = nil
	sess.ToolCache = map[string]json.RawMessage{}
	return m.Store.Save(ctx, sess)
}

// Session returns the raw session (for inspectors and the playground).
func (m *Memory) Session(ctx context.Context) (*storage.Session, error) {
	return m.Load(ctx)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
