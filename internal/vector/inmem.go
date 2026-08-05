package vector

import (
	"context"
	"sort"
	"sync"
)

// InMemoryStore is a brute-force cosine store in RAM. It is the default
// for zero-setup knowledge bases and tests.
type InMemoryStore struct {
	mu          sync.RWMutex
	collections map[string]map[string]Point
}

// NewInMemoryStore builds an empty in-memory vector store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{collections: map[string]map[string]Point{}}
}

func (s *InMemoryStore) EnsureCollection(ctx context.Context, name string, dim int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.collections[name]; !ok {
		s.collections[name] = map[string]Point{}
	}
	return nil
}

func (s *InMemoryStore) Upsert(ctx context.Context, name string, points []Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	col, ok := s.collections[name]
	if !ok {
		col = map[string]Point{}
		s.collections[name] = col
	}
	for _, p := range points {
		if p.ID == "" {
			continue
		}
		col[p.ID] = p
	}
	return nil
}

func (s *InMemoryStore) Search(ctx context.Context, name string, vector []float32, limit int, filter map[string]any) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	col, ok := s.collections[name]
	if !ok {
		return nil, nil
	}
	var results []SearchResult
	for id, p := range col {
		if !matchFilter(p.Payload, filter) {
			continue
		}
		score := Cosine(vector, p.Vector)
		results = append(results, SearchResult{Point: Point{ID: id, Vector: p.Vector, Payload: p.Payload}, Score: score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *InMemoryStore) Delete(ctx context.Context, name string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if col, ok := s.collections[name]; ok {
		for _, id := range ids {
			delete(col, id)
		}
	}
	return nil
}

func (s *InMemoryStore) Count(ctx context.Context, name string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.collections[name]), nil
}

func (s *InMemoryStore) Close() error { return nil }
