// Package storage persists agent sessions (message history, approvals,
// tool-result cache) across runs. Adapters: in-memory, SQLite, Postgres.
package storage

import (
	"context"
	"encoding/json"
	"time"

	"ernest/internal/core"
)

// Session is the persisted state of one conversation.
type Session struct {
	ID        string                `json:"id"`
	AgentName string                `json:"agentName"`
	UserID    string                `json:"userId,omitempty"`
	Messages  []core.Message        `json:"messages"`
	Metadata  map[string]string     `json:"metadata,omitempty"`
	// PendingApprovals is the HITL queue waiting on a human.
	PendingApprovals []core.ApprovalRequest `json:"pendingApprovals,omitempty"`
	// ResolvedApprovals maps approval id -> decision (kept for audit).
	ResolvedApprovals map[string]core.ApprovalDecision `json:"resolvedApprovals,omitempty"`
	// PendingCalls are tool calls blocked on HITL approval.
	PendingCalls []PendingToolCall `json:"pendingCalls,omitempty"`
	// ToolCache maps tool call id -> serialized result so resumed runs
	// replay without re-executing side-effectful tools.
	ToolCache map[string]json.RawMessage `json:"toolCache,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PendingToolCall links a blocked tool call to its approval request.
type PendingToolCall struct {
	ApprovalID string       `json:"approvalId"`
	Call       core.ToolCall `json:"call"`
}

// NewSession builds an empty session.
func NewSession(id, agentName, userID string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:                id,
		AgentName:         agentName,
		UserID:            userID,
		Messages:          []core.Message{},
		Metadata:          map[string]string{},
		PendingApprovals:  []core.ApprovalRequest{},
		ResolvedApprovals: map[string]core.ApprovalDecision{},
		ToolCache:         map[string]json.RawMessage{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// SessionStore is the persistence interface. Implementations must be
// safe for concurrent use.
type SessionStore interface {
	Save(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, agentName string) ([]*Session, error)
	Close() error
}

// SessionStoreOption is a functional option for store constructors.
type SessionStoreOption func(cfg *StoreConfig)

// StoreConfig carries shared store options.
type StoreConfig struct {
	// DSN is the connection string (file path for SQLite, URL for PG).
	DSN string
	// TableName defaults to "ernest_sessions".
	TableName string
}

// WithDSN configures the data source.
func WithDSN(dsn string) SessionStoreOption {
	return func(cfg *StoreConfig) { cfg.DSN = dsn }
}

// WithTableName customizes the backing table.
func WithTableName(name string) SessionStoreOption {
	return func(cfg *StoreConfig) { cfg.TableName = name }
}

func (c *StoreConfig) apply(opts []SessionStoreOption) {
	c.TableName = "ernest_sessions"
	for _, o := range opts {
		o(c)
	}
}
