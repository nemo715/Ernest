// Package knowledge implements retrieval-augmented knowledge bases:
// text chunking, embeddings, and vector-store persistence.
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"ernest/internal/core"
	"ernest/internal/llm"
	"ernest/internal/vector"
)

// Chunk is one piece of embedded text.
type Chunk struct {
	ID       string
	Text     string
	Metadata map[string]any
}

// ChunkResult is a retrieved chunk with its similarity score.
type ChunkResult struct {
	Chunk
	Score float64 `json:"score"`
}

// ChunkOptions controls the chunking pipeline.
type ChunkOptions struct {
	ChunkSize    int // target characters per chunk (default 800)
	ChunkOverlap int // overlap characters between chunks (default 0 = no overlap)
}

// KnowledgeBase combines an embedder and a vector store. It powers
// agent knowledge: content is chunked, embedded, stored, and retrieved
// at query time.
type KnowledgeBase struct {
	Store      vector.VectorStore
	Embedder   llm.Embedder
	Collection string
	ChunkSize  int
	ChunkOverlap int

	// docIDCount is used to build stable chunk ids per document.
	docIDCount int
}

// New builds a knowledge base over a vector store and embedder.
func New(store vector.VectorStore, embedder llm.Embedder, opts ChunkOptions) *KnowledgeBase {
	if store == nil {
		store = vector.NewInMemoryStore()
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 800
	}
	if opts.ChunkOverlap < 0 {
		opts.ChunkOverlap = 0
	}
	if opts.ChunkOverlap >= opts.ChunkSize {
		opts.ChunkOverlap = opts.ChunkSize / 4
	}
	return &KnowledgeBase{
		Store:        store,
		Embedder:     embedder,
		Collection:   "ernest_knowledge",
		ChunkSize:    opts.ChunkSize,
		ChunkOverlap: opts.ChunkOverlap,
	}
}

// SetCollection renames the backing vector collection.
func (kb *KnowledgeBase) SetCollection(name string) *KnowledgeBase {
	kb.Collection = name
	return kb
}

// AddText chunks, embeds and stores text content.
func (kb *KnowledgeBase) AddText(ctx context.Context, text string, metadata map[string]any) ([]string, error) {
	if kb.Embedder == nil {
		return nil, core.NewError(core.KindKnowledge, "knowledge base has no embedder")
	}
	kb.docIDCount++
	docID := fmt.Sprintf("doc_%d", kb.docIDCount)
	chunks := ChunkText(text, kb.ChunkSize, kb.ChunkOverlap)
	ids := make([]string, 0, len(chunks))
	vecs, err := kb.Embedder.Embed(ctx, chunks)
	if err != nil {
		return nil, core.NewError(core.KindKnowledge, "embed failed: "+err.Error(), err)
	}
	if len(vecs) != len(chunks) {
		return nil, core.NewError(core.KindKnowledge, fmt.Sprintf("embedder returned %d vectors for %d chunks", len(vecs), len(chunks)))
	}
	dim := 0
	if len(vecs) > 0 {
		dim = len(vecs[0])
	}
	if err := kb.Store.EnsureCollection(ctx, kb.Collection, dim); err != nil {
		return nil, err
	}
	points := make([]vector.Point, 0, len(chunks))
	for i, c := range chunks {
		id := fmt.Sprintf("%s_chunk_%d", docID, i)
		ids = append(ids, id)
		payload := map[string]any{"text": c}
		for k, v := range metadata {
			payload[k] = v
		}
		payload["doc_id"] = docID
		points = append(points, vector.Point{ID: id, Vector: vecs[i], Payload: payload})
	}
	if err := kb.Store.Upsert(ctx, kb.Collection, points); err != nil {
		return nil, err
	}
	return ids, nil
}

// AddFile loads a text file and ingests it.
func (kb *KnowledgeBase) AddFile(ctx context.Context, path string, metadata map[string]any) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, core.NewError(core.KindKnowledge, "read file: "+err.Error(), err)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["source"] = filepath.Base(path)
	return kb.AddText(ctx, string(data), metadata)
}

// Query embeds the query and retrieves the top-k similar chunks.
func (kb *KnowledgeBase) Query(ctx context.Context, query string, k int) ([]ChunkResult, error) {
	if kb.Embedder == nil {
		return nil, core.NewError(core.KindKnowledge, "knowledge base has no embedder")
	}
	if k <= 0 {
		k = 4
	}
	vecs, err := kb.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, core.NewError(core.KindKnowledge, "embed query: "+err.Error(), err)
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	results, err := kb.Store.Search(ctx, kb.Collection, vecs[0], k, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ChunkResult, 0, len(results))
	for _, r := range results {
		text, _ := r.Payload["text"].(string)
		meta := make(map[string]any, len(r.Payload))
		for k, v := range r.Payload {
			if k != "text" {
				meta[k] = v
			}
		}
		out = append(out, ChunkResult{Chunk: Chunk{ID: r.ID, Text: text, Metadata: meta}, Score: r.Score})
	}
	return out, nil
}

// Delete removes chunks by id.
func (kb *KnowledgeBase) Delete(ctx context.Context, ids []string) error {
	return kb.Store.Delete(ctx, kb.Collection, ids)
}

// Count reports stored chunks.
func (kb *KnowledgeBase) Count(ctx context.Context) (int, error) {
	return kb.Store.Count(ctx, kb.Collection)
}

// ChunkText splits text into overlapping chunks on paragraph and
// sentence boundaries, falling back to hard character splits.
func ChunkText(text string, size, overlap int) []string {
	if size <= 0 {
		size = 800
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Prefer paragraph boundaries for a stable split.
	paragraphs := splitParagraphs(text)
	var chunks []string
	var buf strings.Builder
	flush := func() {
		if strings.TrimSpace(buf.String()) != "" {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
		}
		buf.Reset()
	}
	for _, p := range paragraphs {
		if buf.Len() > 0 && buf.Len()+len(p) > size {
			flush()
		}
		if len(p) > size {
			// Hard split long paragraphs, then re-chunk with overlap.
			subs := hardSplit(p, size, overlap)
			for _, s := range subs {
				if buf.Len() > 0 && buf.Len()+len(s) > size {
					flush()
				}
				if buf.Len() > 0 {
					buf.WriteString("\n")
				}
				buf.WriteString(s)
				if buf.Len() >= size {
					flush()
				}
			}
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(p)
		if buf.Len() >= size {
			flush()
		}
	}
	flush()
	return chunks
}

func splitParagraphs(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			out = append(out, strings.TrimSpace(cur.String()))
		}
		cur.Reset()
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString(" ")
		}
		cur.WriteString(trimmed)
	}
	flush()
	return out
}

func hardSplit(s string, size, overlap int) []string {
	if len(s) <= size {
		return []string{s}
	}
	var out []string
	start := 0
	for start < len(s) {
		end := start + size
		if end >= len(s) {
			out = append(out, s[start:])
			break
		}
		// Extend to the next sentence end if close enough.
		if idx := nextSentenceBoundary(s[end:], size/4); idx >= 0 {
			end += idx
		}
		out = append(out, s[start:end])
		if overlap <= 0 {
			start = end
		} else {
			start = end - overlap
			if start <= end-size {
				start = end
			}
		}
	}
	return out
}

func nextSentenceBoundary(s string, maxLook int) int {
	if len(s) > maxLook {
		s = s[:maxLook]
	}
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' {
			// Skip "3.14" style decimals (char before dot is a digit).
			if r == '.' && i > 0 && unicode.IsDigit(rune(s[i-1])) {
				continue
			}
			return i + 1
		}
	}
	return -1
}

// ChunkJSON is a helper to serialise chunks for the wire protocol.
func ChunkJSON(chunks []ChunkResult) ([]byte, error) {
	return json.Marshal(chunks)
}
