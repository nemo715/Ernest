package storage

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

// InMemoryStore keeps sessions in RAM. It is the default store: zero
// setup, perfect for tests, prototypes and short-lived processes.
type InMemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	feedback map[string][]*RunFeedback
}

// NewInMemoryStore builds an empty in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{sessions: map[string]*Session{}, feedback: map[string][]*RunFeedback{}}
}

func (s *InMemoryStore) Save(ctx context.Context, sess *Session) error {
	if sess.ID == "" {
		return core.NewError(core.KindMemory, "session id is required")
	}
	copySess := *sess
	messages := make([]core.Message, len(sess.Messages))
	copy(messages, sess.Messages)
	copySess.Messages = messages
	s.mu.Lock()
	s.sessions[sess.ID] = &copySess
	s.mu.Unlock()
	return nil
}

func (s *InMemoryStore) Get(ctx context.Context, id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, core.NewError(core.KindMemory, "session not found: "+id)
	}
	copySess := *sess
	messages := make([]core.Message, len(sess.Messages))
	copy(messages, sess.Messages)
	copySess.Messages = messages
	return &copySess, nil
}

func (s *InMemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}

func (s *InMemoryStore) List(ctx context.Context, agentName string) ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*Session{}
	for _, sess := range s.sessions {
		if agentName == "" || sess.AgentName == agentName {
			sessCopy := *sess
			messages := make([]core.Message, len(sess.Messages))
			copy(messages, sess.Messages)
			sessCopy.Messages = messages
			out = append(out, &sessCopy)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *InMemoryStore) Close() error { return nil }

func (s *InMemoryStore) SaveFeedback(ctx context.Context, f *RunFeedback) error {
	if f.RunID == "" {
		return core.NewError(core.KindMemory, "feedback run id is required")
	}
	if f.CreatedAt == "" {
		f.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.mu.Lock()
	s.feedback[f.RunID] = append(s.feedback[f.RunID], f)
	s.mu.Unlock()
	return nil
}

func (s *InMemoryStore) ListFeedback(ctx context.Context, runID string) ([]*RunFeedback, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RunFeedback, 0, len(s.feedback[runID]))
	for _, f := range s.feedback[runID] {
		c := *f
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}
