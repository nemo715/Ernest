// Package vector implements vector stores for knowledge bases and
// semantic memory. Adapters: in-memory (default) and Qdrant over its
// REST API (no SDK dependency).
package vector

import (
	"context"
	"fmt"
	"math"
)

// Point is one vector with optional payload metadata.
type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

// SearchResult is a match with its similarity score (cosine, 0..1).
type SearchResult struct {
	Point
	Score float64 `json:"score"`
}

// VectorStore is the persistence interface. Implementations must be
// safe for concurrent use.
type VectorStore interface {
	// EnsureCollection creates the collection if missing.
	EnsureCollection(ctx context.Context, name string, dim int) error
	Upsert(ctx context.Context, name string, points []Point) error
	Search(ctx context.Context, name string, vector []float32, limit int, filter map[string]any) ([]SearchResult, error)
	Delete(ctx context.Context, name string, ids []string) error
	Count(ctx context.Context, name string) (int, error)
	Close() error
}

// Cosine computes cosine similarity (1 = identical).
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Normalize returns a copy of v scaled to unit length.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return v
	}
	n := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(float64(f) / n)
	}
	return out
}

// matchFilter reports whether payload satisfies a flat filter map
// {key: value} (all entries must match; values compare via ==).
func matchFilter(payload, filter map[string]any) bool {
	if len(filter) == 0 {
		return true
	}
	for k, want := range filter {
		got, ok := payload[k]
		if !ok {
			return false
		}
		switch w := want.(type) {
		case float64:
			f, ok := got.(float64)
			if !ok {
				return false
			}
			if math.Abs(f-w) > 1e-9 {
				return false
			}
		default:
			if fmt.Sprint(got) != fmt.Sprint(want) {
				return false
			}
		}
	}
	return true
}
