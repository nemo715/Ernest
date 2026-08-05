package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"ernest/internal/core"
)

// roundtrip asserts full session fidelity through any SessionStore:
// messages, pending calls, approvals and the tool-result cache.
func roundtrip(t *testing.T, s SessionStore) {
	t.Helper()
	ctx := context.Background()
	sess := NewSession("s1", "agent-a", "user-1")
	sess.Messages = append(sess.Messages, core.NewUserMessage("hello"))
	sess.PendingCalls = []PendingToolCall{{ApprovalID: "ap1", Call: core.ToolCall{ID: "c1", Name: "send_email", Arguments: []byte(`{"to":"x"}`)}}}
	sess.PendingApprovals = []core.ApprovalRequest{{ID: "ap1", Action: "send_email", Status: "pending"}}
	sess.ToolCache = map[string]json.RawMessage{"c1": json.RawMessage(`{"sent":true}`)}
	if err := s.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentName != "agent-a" || got.UserID != "user-1" {
		t.Fatalf("identity lost: %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Text() != "hello" {
		t.Fatalf("messages lost: %+v", got.Messages)
	}
	if len(got.PendingCalls) != 1 || got.PendingCalls[0].ApprovalID != "ap1" || got.PendingCalls[0].Call.ID != "c1" {
		t.Fatalf("pending calls lost: %+v", got.PendingCalls)
	}
	if len(got.PendingApprovals) != 1 || got.PendingApprovals[0].ID != "ap1" {
		t.Fatalf("pending approvals lost: %+v", got.PendingApprovals)
	}
	if string(got.ToolCache["c1"]) != `{"sent":true}` {
		t.Fatalf("tool cache lost: %+v", got.ToolCache)
	}

	// Get on a missing session fails with a typed error.
	if _, err := s.Get(ctx, "nope"); err == nil {
		t.Fatal("missing session must error")
	}
}

func TestInMemoryRoundtrip(t *testing.T) {
	s := NewInMemoryStore()
	defer s.Close()
	roundtrip(t, s)
}

func TestSQLiteRoundtrip(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	roundtrip(t, s)
}

func TestSQLiteMemoryDSN(t *testing.T) {
	// ":memory:" is a valid DSN — no file needed.
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	roundtrip(t, s)
}

func TestSQLiteUpdateOverwrites(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "upd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	sess := NewSession("u1", "agent", "u")
	sess.Messages = append(sess.Messages, core.NewUserMessage("first"))
	if err := s.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	sess.Messages = append(sess.Messages, core.NewUserMessage("second"))
	if err := s.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("update lost messages: %d", len(got.Messages))
	}
	if got.Messages[1].Text() != "second" {
		t.Fatalf("update order wrong: %+v", got.Messages)
	}
}

func TestSQLiteListAndDelete(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		sess := NewSession(id, "agent-x", "u")
		if err := s.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("list all = %d, %v", len(all), err)
	}
	filtered, err := s.List(ctx, "agent-x")
	if err != nil || len(filtered) != 3 {
		t.Fatalf("list filtered = %d, %v", len(filtered), err)
	}
	other, err := s.List(ctx, "other")
	if err != nil || len(other) != 0 {
		t.Fatalf("list other = %d, %v", len(other), err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "a"); err == nil {
		t.Fatal("deleted session still readable")
	}
}

func TestInMemorySaveRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	defer s.Close()
	if err := s.Save(context.Background(), &Session{}); err == nil {
		t.Fatal("empty session id must error")
	}
}

func TestInMemoryListNewestFirst(t *testing.T) {
	s := NewInMemoryStore()
	defer s.Close()
	ctx := context.Background()
	old := NewSession("old", "a", "")
	old.UpdatedAt = time.Now().Add(-time.Hour)
	recent := NewSession("new", "a", "")
	if err := s.Save(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, recent); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "new" || all[1].ID != "old" {
		t.Fatalf("list order = %s, %s", all[0].ID, all[1].ID)
	}
}

func TestInMemoryGetCopies(t *testing.T) {
	// Mutating the returned session must not corrupt the store.
	s := NewInMemoryStore()
	defer s.Close()
	ctx := context.Background()
	sess := NewSession("s1", "a", "")
	sess.Messages = append(sess.Messages, core.NewUserMessage("keep"))
	if err := s.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "s1")
	got.Messages = append(got.Messages, core.NewUserMessage("polluted"))
	got2, _ := s.Get(ctx, "s1")
	if len(got2.Messages) != 1 {
		t.Fatalf("store corrupted: %d messages", len(got2.Messages))
	}
}

func TestFileName(t *testing.T) {
	cases := map[string]string{
		"":         "",
		":memory:": ":memory:",
		"foo.db":   "foo.db",
		"foo":      "foo.db",
	}
	for in, want := range cases {
		if got := FileName(in); got != want {
			t.Fatalf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCustomTableName(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "custom.db"), WithTableName("my_sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Save(ctx, NewSession("c1", "a", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
}
