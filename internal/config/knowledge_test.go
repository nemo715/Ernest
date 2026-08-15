package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/core"
)

func writeConfig(t *testing.T, dir, jsonText string) string {
	t.Helper()
	path := filepath.Join(dir, "ernest.json")
	if err := os.WriteFile(path, []byte(jsonText), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestKnowledgeValidationRules(t *testing.T) {
	cases := []struct {
		name    string
		cfg     string
		wantErr string
	}{
		{
			name: "knowledge without embeddings on non-mock provider",
			cfg: `{
				"agents": [{"name": "a", "provider": "openai", "model": "gpt-4o-mini",
					"knowledge": {"sources": ["docs"]}}]
			}`,
			wantErr: "embeddings",
		},
		{
			name: "knowledge with mock provider needs no embeddings block",
			cfg: `{
				"agents": [{"name": "a", "provider": "mock", "model": "mock-1",
					"knowledge": {"sources": ["docs"]}}]
			}`,
			wantErr: "",
		},
		{
			name: "embeddings compatible without baseUrl",
			cfg: `{
				"embeddings": {"provider": "compatible", "model": "text-embedding-3-small"},
				"agents": [{"name": "a", "provider": "openai", "model": "gpt-4o-mini",
					"knowledge": {"sources": ["docs"]}}]
			}`,
			wantErr: "baseUrl",
		},
		{
			name: "embeddings unknown provider",
			cfg: `{
				"embeddings": {"provider": "wat"},
				"agents": [{"name": "a", "provider": "openai", "model": "gpt-4o-mini",
					"knowledge": {"sources": ["docs"]}}]
			}`,
			wantErr: "unknown provider",
		},
		{
			name: "knowledge without sources",
			cfg: `{
				"agents": [{"name": "a", "provider": "mock", "model": "mock-1",
					"knowledge": {"sources": []}}]
			}`,
			wantErr: "at least one source",
		},
		{
			name: "knowledge topK out of range",
			cfg: `{
				"agents": [{"name": "a", "provider": "mock", "model": "mock-1",
					"knowledge": {"sources": ["docs"], "topK": 25}}]
			}`,
			wantErr: "topK",
		},
		{
			name: "semanticMemory needs embeddings on non-mock provider",
			cfg: `{
				"agents": [{"name": "a", "provider": "openai", "model": "gpt-4o-mini",
					"semanticMemory": true}]
			}`,
			wantErr: "embeddings",
		},
		{
			name: "valid openai embeddings block",
			cfg: `{
				"embeddings": {"provider": "openai", "model": "text-embedding-3-small"},
				"agents": [{"name": "a", "provider": "openai", "model": "gpt-4o-mini",
					"knowledge": {"sources": ["docs"], "topK": 2}}]
			}`,
			wantErr: "",
		},
		{
			name: "valid compatible embeddings block",
			cfg: `{
				"embeddings": {"provider": "compatible", "baseUrl": "https://openrouter.ai/api/v1",
					"model": "text-embedding-3-small", "apiKeyEnv": "OPENROUTER_API_KEY"},
				"agents": [{"name": "a", "provider": "compatible", "model": "gpt-4o-mini",
					"baseUrl": "https://openrouter.ai/api/v1",
					"knowledge": {"sources": ["docs"]}}]
			}`,
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, t.TempDir(), tc.cfg))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ingestion + retrieval (mock embeddings, no keys)
// ---------------------------------------------------------------------------

func TestKnowledgeIngestAndRetrieve(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pricing.md": "Ernest pricing: the enterprise plan costs 999 dollars per month and includes unlimited agents.",
		"quantum.md": "Qiskit circuits run on the Aer simulator; readout errors are corrected with an assignment matrix.",
		"ignored.txt": "This plain text file should also be ingested.",
	}
	names := []string{"pricing.md", "quantum.md", "ignored.txt"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(docs, n), []byte(files[n]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{
		"agents": [{"name": "a", "provider": "mock", "model": "mock-1",
			"knowledge": {"sources": ["docs"]}}]
	}`
	c, err := Load(writeConfig(t, dir, cfg))
	if err != nil {
		t.Fatal(err)
	}
	// Relative source resolves against the config file directory: the
	// temp config lives in the same dir as docs/.
	rt, err := c.Build(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if len(rt.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(rt.Agents))
	}
	ag := rt.Agents[0]
	if ag.Knowledge == nil {
		t.Fatal("agent.Knowledge not attached")
	}
	if ag.Knowledge.TopK != 4 {
		t.Fatalf("default TopK = %d, want 4", ag.Knowledge.TopK)
	}
	n, err := ag.Knowledge.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 chunks ingested, got %d", n)
	}
	// Word-overlap mock embeddings: the query must retrieve the pricing
	// chunk above the quantum chunk.
	results, err := ag.Knowledge.Query(context.Background(), "enterprise pricing 999 dollars per month", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Score < results[1].Score {
		t.Fatalf("pricing chunk should rank above quantum: %+v", results)
	}
	if !strings.Contains(results[0].Text, "pricing") {
		t.Fatalf("top result should be the pricing chunk, got %q", results[0].Text)
	}
}

func TestKnowledgeTopKConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "docs", "doc.md")
		if err := os.WriteFile(name, []byte("Ernest pricing plans cost 999 dollars monthly tier "), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{
		"agents": [{"name": "a", "provider": "mock", "model": "mock-1",
			"knowledge": {"sources": ["docs"], "topK": 1}}]
	}`
	c, err := Load(writeConfig(t, dir, cfg))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := c.Build(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	results, err := rt.Agents[0].Knowledge.Query(context.Background(), "pricing", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("topK=1 must return exactly 1 chunk, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Semantic memory tools
// ---------------------------------------------------------------------------

func TestSemanticMemoryTools(t *testing.T) {
	cfg := `{
		"agents": [{"name": "a", "provider": "mock", "model": "mock-1", "semanticMemory": true}]
	}`
	c, err := Load(writeConfig(t, t.TempDir(), cfg))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := c.Build(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ag := rt.Agents[0]
	if ag.SemanticMemory == nil {
		t.Fatal("agent.SemanticMemory not attached")
	}
	byName := map[string]bool{}
	for _, t := range ag.Tools {
		byName[t.Name] = true
	}
	if !byName["remember"] || !byName["recall"] {
		t.Fatalf("expected remember/recall tools, got %v", byName)
	}

	// remember -> recall roundtrip through the real tool functions.
	tc := core.NewToolContext("a", "run_1")
	var remembered map[string]any
	for _, tool := range ag.Tools {
		if tool.Name == "remember" {
			res, err := tool.Run(context.Background(), tc, []byte(`{"text": "The production server runs on port 9293"}`))
			if err != nil {
				t.Fatal(err)
			}
			remembered = res.(map[string]any)
		}
	}
	if remembered == nil {
		t.Fatal("remember did not run")
	}
	if remembered["remembered"].(int) != 1 {
		t.Fatalf("expected 1 chunk remembered, got %v", remembered)
	}

	for _, tool := range ag.Tools {
		if tool.Name == "recall" {
			res, err := tool.Run(context.Background(), tc, []byte(`{"query": "which port does the production server run on", "k": 3}`))
			if err != nil {
				t.Fatal(err)
			}
			out := res.(map[string]any)["results"].([]map[string]any)
			if len(out) == 0 {
				t.Fatal("recall returned nothing")
			}
			if !strings.Contains(out[0]["text"].(string), "9293") {
				t.Fatalf("top recall should contain the remembered fact, got %+v", out[0])
			}
		}
	}
}

func TestSemanticMemoryToolValidation(t *testing.T) {
	cfg := `{
		"agents": [{"name": "a", "provider": "mock", "model": "mock-1", "semanticMemory": true}]
	}`
	c, err := Load(writeConfig(t, t.TempDir(), cfg))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := c.Build(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	tc := core.NewToolContext("a", "run_1")
	for _, tool := range rt.Agents[0].Tools {
		switch tool.Name {
		case "remember":
			if _, err := tool.Run(context.Background(), tc, []byte(`{}`)); err == nil {
				t.Fatal("remember with no text must fail")
			}
		case "recall":
			if _, err := tool.Run(context.Background(), tc, []byte(`{}`)); err == nil {
				t.Fatal("recall with no query must fail")
			}
		}
	}
}
