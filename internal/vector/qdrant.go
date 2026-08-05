package vector

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

// QdrantConfig configures the Qdrant REST client.
type QdrantConfig struct {
	BaseURL    string // e.g. http://localhost:6333
	APIKey     string // optional
	Distance   string // Cosine (default) | Dot | Euclid
	HTTPClient *http.Client
}

type qdrantStore struct {
	cfg  QdrantConfig
	http *http.Client
}

// NewQdrantStore connects to a Qdrant server over its REST API.
// The collection is created lazily on first write (EnsureCollection).
func NewQdrantStore(cfg QdrantConfig) *qdrantStore {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:6333"
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.Distance == "" {
		cfg.Distance = "Cosine"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &qdrantStore{cfg: cfg, http: cfg.HTTPClient}
}

func (s *qdrantStore) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return core.NewError(core.KindKnowledge, "qdrant marshal: "+err.Error(), err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.BaseURL+path, body)
	if err != nil {
		return core.NewError(core.KindKnowledge, "qdrant request: "+err.Error(), err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		req.Header.Set("api-key", s.cfg.APIKey)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return core.NewError(core.KindKnowledge, "qdrant connect: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return core.NewError(core.KindKnowledge, fmt.Sprintf("qdrant HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return core.NewError(core.KindKnowledge, "qdrant decode: "+err.Error(), err)
		}
	}
	return nil
}

func (s *qdrantStore) EnsureCollection(ctx context.Context, name string, dim int) error {
	// Check existence first (GET is cheaper than a 400 on create).
	var exists struct {
		Result bool `json:"result"`
	}
	if err := s.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(name), nil, &exists); err == nil && exists.Result {
		return nil
	}
	payload := map[string]any{
		"vectors": map[string]any{"size": dim, "distance": s.cfg.Distance},
	}
	var out map[string]any
	return s.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name), payload, &out)
}

func (s *qdrantStore) Upsert(ctx context.Context, name string, points []Point) error {
	type qdrantPoint struct {
		ID      string         `json:"id"`
		Vector  []float32      `json:"vector"`
		Payload map[string]any `json:"payload,omitempty"`
	}
	qp := make([]qdrantPoint, 0, len(points))
	for _, p := range points {
		qp = append(qp, qdrantPoint{ID: p.ID, Vector: p.Vector, Payload: p.Payload})
	}
	payload := map[string]any{"points": qp}
	var out map[string]any
	return s.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name)+"/points?wait=true", payload, &out)
}

func (s *qdrantStore) Search(ctx context.Context, name string, vec []float32, limit int, filter map[string]any) ([]SearchResult, error) {
	payload := map[string]any{"vector": vec, "limit": limit, "with_payload": true}
	if len(filter) > 0 {
		payload["filter"] = filterToQdrant(filter)
	}
	var out struct {
		Result []struct {
			ID      string         `json:"id"`
			Version int            `json:"version"`
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(name)+"/points/search", payload, &out); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(out.Result))
	for _, r := range out.Result {
		results = append(results, SearchResult{
			Point: Point{ID: r.ID, Payload: r.Payload},
			Score: r.Score,
		})
	}
	return results, nil
}

// filterToQdrant converts a flat {key: value} filter into a Qdrant
// "must" match filter. Numbers and booleans use match.value; everything
// else uses keyword match.
func filterToQdrant(filter map[string]any) map[string]any {
	must := []map[string]any{}
	for k, v := range filter {
		switch val := v.(type) {
		case float64, bool:
			must = append(must, map[string]any{"key": k, "match": map[string]any{"value": val}})
		default:
			must = append(must, map[string]any{"key": k, "match": map[string]any{"value": fmt.Sprint(val)}})
		}
	}
	return map[string]any{"must": must}
}

func (s *qdrantStore) Delete(ctx context.Context, name string, ids []string) error {
	payload := map[string]any{"points": ids}
	var out map[string]any
	return s.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(name)+"/points/delete?wait=true", payload, &out)
}

func (s *qdrantStore) Count(ctx context.Context, name string) (int, error) {
	var out struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(name)+"/points/count", map[string]any{"exact": true}, &out); err != nil {
		return 0, err
	}
	return out.Result.Count, nil
}

func (s *qdrantStore) Close() error { return nil }
