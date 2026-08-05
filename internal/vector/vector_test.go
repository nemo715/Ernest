package vector

import (
	"context"
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if c := Cosine(a, b); math.Abs(c) > 1e-9 {
		t.Fatalf("orthogonal cosine = %v", c)
	}
	if c := Cosine(a, a); math.Abs(c-1) > 1e-9 {
		t.Fatalf("self cosine = %v", c)
	}
	if c := Cosine([]float32{2, 0}, []float32{1, 0}); math.Abs(c-1) > 1e-9 {
		t.Fatalf("collinear cosine = %v", c)
	}
	// Dimension mismatch -> 0.
	if c := Cosine([]float32{1}, []float32{1, 2}); c != 0 {
		t.Fatalf("mismatch cosine = %v", c)
	}
	// Empty vectors -> 0.
	if c := Cosine(nil, nil); c != 0 {
		t.Fatalf("empty cosine = %v", c)
	}
	// Zero vector -> 0.
	if c := Cosine([]float32{0, 0}, []float32{1, 1}); c != 0 {
		t.Fatalf("zero cosine = %v", c)
	}
}

func TestNormalize(t *testing.T) {
	v := Normalize([]float32{3, 4})
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("normalize = %v", v)
	}
	if math.Abs(math.Sqrt(float64(v[0]*v[0]+v[1]*v[1]))-1) > 1e-6 {
		t.Fatalf("unit length violated: %v", v)
	}
	// Zero vector is returned unchanged, not NaN.
	z := Normalize([]float32{0, 0})
	if len(z) != 2 || z[0] != 0 || math.IsNaN(float64(z[1])) {
		t.Fatalf("zero normalize = %v", z)
	}
	// Input must not be mutated.
	in := []float32{3, 4}
	Normalize(in)
	if in[0] != 3 || in[1] != 4 {
		t.Fatalf("input mutated: %v", in)
	}
}

func TestInMemorySearch(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	if err := s.EnsureCollection(ctx, "c", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "c", []Point{
		{ID: "p1", Vector: []float32{1, 0}, Payload: map[string]any{"tag": "a"}},
		{ID: "p2", Vector: []float32{0.9, 0.1}, Payload: map[string]any{"tag": "a"}},
		{ID: "p3", Vector: []float32{0, 1}, Payload: map[string]any{"tag": "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Search(ctx, "c", []float32{1, 0}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d", len(res))
	}
	if res[0].ID != "p1" || res[1].ID != "p2" {
		t.Fatalf("order = %s, %s", res[0].ID, res[1].ID)
	}
	if res[0].Score <= res[1].Score {
		t.Fatalf("scores not descending: %v %v", res[0].Score, res[1].Score)
	}
	if res[0].Score > 1 || res[0].Score < 0 {
		t.Fatalf("score out of range: %v", res[0].Score)
	}

	// Filter narrows to tag=a only.
	res, err = s.Search(ctx, "c", []float32{1, 0}, 10, map[string]any{"tag": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("filtered results = %d", len(res))
	}
	for _, r := range res {
		if r.Payload["tag"] != "a" {
			t.Fatalf("filter leaked %v", r.Payload)
		}
	}

	// Filter with a missing key matches nothing.
	res, err = s.Search(ctx, "c", []float32{1, 0}, 10, map[string]any{"nope": 1})
	if err != nil || len(res) != 0 {
		t.Fatalf("missing-key filter: %v, %v", res, err)
	}

	// Numeric filter compares with tolerance.
	res, err = s.Search(ctx, "c", []float32{1, 0}, 10, map[string]any{"score_f": 2.5})
	if err != nil || len(res) != 0 {
		t.Fatalf("numeric filter: %v, %v", res, err)
	}

	// Missing collection returns empty, not an error.
	res, err = s.Search(ctx, "nope", []float32{1, 0}, 10, nil)
	if err != nil || len(res) != 0 {
		t.Fatalf("missing collection: %v, %v", res, err)
	}
}

func TestInMemoryUpsertDeleteCount(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	if err := s.EnsureCollection(ctx, "c", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "c", []Point{
		{ID: "x", Vector: []float32{1, 2, 3}},
		{ID: "y", Vector: []float32{4, 5, 6}},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Count(ctx, "c")
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// Upsert replaces by id (no duplicate).
	if err := s.Upsert(ctx, "c", []Point{{ID: "x", Vector: []float32{9, 9, 9}}}); err != nil {
		t.Fatal(err)
	}
	n, _ = s.Count(ctx, "c")
	if n != 2 {
		t.Fatalf("count after replace = %d", n)
	}
	res, _ := s.Search(ctx, "c", []float32{9, 9, 9}, 1, nil)
	if len(res) != 1 || res[0].ID != "x" || res[0].Score < 0.999 {
		t.Fatalf("replace search = %+v", res)
	}

	// Empty id is skipped silently.
	if err := s.Upsert(ctx, "c", []Point{{ID: "", Vector: []float32{1}}}); err != nil {
		t.Fatal(err)
	}
	n, _ = s.Count(ctx, "c")
	if n != 2 {
		t.Fatalf("empty id counted: %d", n)
	}

	// Delete removes ids, missing ids are fine.
	if err := s.Delete(ctx, "c", []string{"x", "missing"}); err != nil {
		t.Fatal(err)
	}
	n, _ = s.Count(ctx, "c")
	if n != 1 {
		t.Fatalf("count after delete = %d", n)
	}

	// Count on a missing collection is 0.
	n, _ = s.Count(ctx, "ghost")
	if n != 0 {
		t.Fatalf("ghost count = %d", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInMemoryNoEnsureNeeded(t *testing.T) {
	// Upsert auto-creates the collection (zero-setup usage).
	ctx := context.Background()
	s := NewInMemoryStore()
	if err := s.Upsert(ctx, "auto", []Point{{ID: "a", Vector: []float32{1}}}); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Count(ctx, "auto")
	if n != 1 {
		t.Fatalf("auto collection count = %d", n)
	}
}
