package knowledge

import (
	"context"
	"strings"
	"testing"

	"ernest/internal/llm"
	"ernest/internal/vector"
)

func TestChunkTextEmptyAndShort(t *testing.T) {
	if c := ChunkText("", 800, 100); c != nil {
		t.Fatalf("empty text = %v", c)
	}
	if c := ChunkText("   \n\n  ", 800, 100); c != nil {
		t.Fatalf("whitespace text = %v", c)
	}
	c := ChunkText("Hello world.", 800, 100)
	if len(c) != 1 || c[0] != "Hello world." {
		t.Fatalf("short text = %v", c)
	}
}

func TestChunkTextMergesParagraphs(t *testing.T) {
	text := "First paragraph about ernest.\n\nSecond paragraph about agents."
	c := ChunkText(text, 800, 100)
	if len(c) != 1 {
		t.Fatalf("chunks = %v", c)
	}
	if !strings.Contains(c[0], "First paragraph") || !strings.Contains(c[0], "Second paragraph") {
		t.Fatalf("paragraphs merged wrong: %q", c[0])
	}
}

func TestChunkTextSeparatesParagraphs(t *testing.T) {
	// Two paragraphs each larger than half the chunk size must split.
	text := strings.Repeat("paragraph one alpha ", 12) + "\n\n" + strings.Repeat("paragraph two beta ", 12)
	c := ChunkText(text, 100, 0)
	if len(c) < 2 {
		t.Fatalf("expected split, got %d chunks: %v", len(c), c)
	}
	joined := strings.Join(c, " ")
	if !strings.Contains(joined, "paragraph one") || !strings.Contains(joined, "paragraph two") {
		t.Fatalf("text lost: %q", joined)
	}
}

func TestChunkTextHardSplit(t *testing.T) {
	long := strings.Repeat("abcdefghij", 100) // 1000 chars, no boundaries
	c := ChunkText(long, 200, 0)
	if len(c) < 5 {
		t.Fatalf("hard split produced %d chunks", len(c))
	}
	total := 0
	for _, s := range c {
		total += len(s)
		if len(s) > 200 {
			t.Fatalf("chunk exceeds size: %d", len(s))
		}
	}
	if total < 1000 {
		t.Fatalf("lost text: %d of 1000", total)
	}
}

func TestChunkTextOverlap(t *testing.T) {
	long := strings.Repeat("x", 500)
	c := ChunkText(long, 100, 25)
	if len(c) < 5 {
		t.Fatalf("overlap chunks = %d", len(c))
	}
	// The next chunk must start with the previous chunk's tail (overlap).
	prevSuffix := c[0][len(c[0])-25:]
	if !strings.HasPrefix(c[1], prevSuffix) {
		t.Fatalf("overlap missing: %q vs %q", c[0], c[1])
	}
}

func TestChunkTextDefaults(t *testing.T) {
	c := ChunkText("x", 0, 0) // defaults to size 800
	if len(c) != 1 || c[0] != "x" {
		t.Fatalf("defaults = %v", c)
	}
}

func TestKnowledgeAddQuery(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{EmbedDim: 16})
	kb := New(vector.NewInMemoryStore(), p, ChunkOptions{})
	ctx := context.Background()
	text := "Ernest is the fastest agent framework with memory and tools."
	ids, err := kb.AddText(ctx, text, map[string]any{"topic": "ernest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	n, err := kb.Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// Querying with the identical text must find the chunk (score ~1).
	res, err := kb.Query(ctx, text, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != ids[0] {
		t.Fatalf("results = %+v", res)
	}
	if res[0].Score < 0.999 {
		t.Fatalf("score = %v", res[0].Score)
	}
	if res[0].Text != text {
		t.Fatalf("text = %q", res[0].Text)
	}
	if res[0].Metadata["topic"] != "ernest" || res[0].Metadata["doc_id"] == "" {
		t.Fatalf("metadata = %+v", res[0].Metadata)
	}
}

func TestKnowledgeMultiChunk(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{EmbedDim: 8})
	kb := New(vector.NewInMemoryStore(), p, ChunkOptions{ChunkSize: 50, ChunkOverlap: 10})
	ctx := context.Background()
	text := "Ernest has memory, knowledge, tools and teams." + strings.Repeat(" more content here for chunking purposes ", 10)
	ids, err := kb.AddText(ctx, text, map[string]any{"source": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(ids))
	}
	n, _ := kb.Count(ctx)
	if n != len(ids) {
		t.Fatalf("count %d != ids %d", n, len(ids))
	}
	res, err := kb.Query(ctx, "memory knowledge tools teams", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no results")
	}
	for _, r := range res {
		if r.Metadata["source"] != "test" {
			t.Fatalf("metadata lost: %+v", r.Metadata)
		}
	}
}

func TestKnowledgeDelete(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{EmbedDim: 8})
	kb := New(vector.NewInMemoryStore(), p, ChunkOptions{})
	ctx := context.Background()
	ids, err := kb.AddText(ctx, "alpha content", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := kb.Delete(ctx, ids); err != nil {
		t.Fatal(err)
	}
	n, _ := kb.Count(ctx)
	if n != 0 {
		t.Fatalf("count after delete = %d", n)
	}
}

func TestKnowledgeNoEmbedder(t *testing.T) {
	kb := New(vector.NewInMemoryStore(), nil, ChunkOptions{})
	if _, err := kb.AddText(context.Background(), "x", nil); err == nil {
		t.Fatal("add without embedder must error")
	}
	if _, err := kb.Query(context.Background(), "x", 1); err == nil {
		t.Fatal("query without embedder must error")
	}
}

func TestKnowledgeEmbedError(t *testing.T) {
	p := llm.NewMock(llm.MockConfig{EmbedErr: errBoom})
	kb := New(vector.NewInMemoryStore(), p, ChunkOptions{})
	if _, err := kb.AddText(context.Background(), "x", nil); err == nil {
		t.Fatal("embed failure must surface")
	}
}

func TestKnowledgeDefaults(t *testing.T) {
	// Defaults: in-memory store, 800-size chunking, no overlap,
	// ernest_knowledge collection.
	kb := New(nil, llm.NewMock(llm.MockConfig{}), ChunkOptions{})
	if kb.Collection != "ernest_knowledge" || kb.ChunkSize != 800 || kb.ChunkOverlap != 0 {
		t.Fatalf("defaults = %+v", kb)
	}
	kb2 := New(nil, nil, ChunkOptions{ChunkSize: 50, ChunkOverlap: 40})
	if kb2.ChunkOverlap >= kb2.ChunkSize {
		t.Fatalf("overlap not clamped: %+v", kb2)
	}
	if kb2.SetCollection("custom").Collection != "custom" {
		t.Fatal("SetCollection failed")
	}
}

var errBoom = errBoomImpl{}

type errBoomImpl struct{}

func (errBoomImpl) Error() string { return "embed boom" }
