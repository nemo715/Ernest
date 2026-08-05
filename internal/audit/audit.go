// Package audit records a bounded, in-memory trail of agent actions for
// compliance and debugging: tool calls, approval decisions and run
// outcomes. It is wired into the server via agent hooks and the approve
// handler, and exposed at GET /api/audit.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// DefaultMaxEntries is the ring buffer size.
const DefaultMaxEntries = 2000

// Entry is one immutable audit record.
type Entry struct {
	ID     string          `json:"id"`
	Time   time.Time       `json:"time"`
	Kind   string          `json:"kind"` // tool.call | approval.decided | run.complete | run.failed | run.interrupted
	Agent  string          `json:"agent"`
	RunID  string          `json:"runId,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Auditor is a thread-safe ring buffer of the last max entries.
type Auditor struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	max     int
}

// New builds an auditor with the default capacity.
func New() *Auditor {
	return NewWithMax(DefaultMaxEntries)
}

// NewWithMax builds an auditor holding at most max entries.
func NewWithMax(max int) *Auditor {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	return &Auditor{max: max}
}

// Record appends an entry (always succeeds; oldest entries are evicted).
func (a *Auditor) Record(kind, agent, runID string, detail any) {
	data, _ := json.Marshal(detail)
	e := Entry{
		ID:     newAuditID(),
		Time:   time.Now().UTC(),
		Kind:   kind,
		Agent:  agent,
		RunID:  runID,
		Detail: data,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) < a.max {
		a.entries = append(a.entries, e)
		return
	}
	a.entries[a.next] = e
	a.next = (a.next + 1) % a.max
}

// List returns the newest n entries in chronological order (n <= 0 = all).
func (a *Auditor) List(n int) []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Entry, 0, len(a.entries))
	if len(a.entries) == a.max {
		out = append(out, a.entries[a.next:]...)
		out = append(out, a.entries[:a.next]...)
	} else {
		out = append(out, a.entries...)
	}
	if n > 0 && n < len(out) {
		out = out[len(out)-n:]
	}
	return out
}

func newAuditID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "au_" + hex.EncodeToString(b)
}
