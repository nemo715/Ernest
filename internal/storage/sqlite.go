package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"github.com/nemo715/Ernest/internal/core"
)

// SQLiteStore persists sessions to a SQLite file using the pure-Go
// modernc driver — no native compilation, safe on every platform.
type SQLiteStore struct {
	db  *sql.DB
	ctx context.Context
	cnl context.CancelFunc
	table string
}

// NewSQLiteStore opens (and initialises) a SQLite-backed store.
// dsn is a file path, e.g. "ernest.db" or ":memory:".
func NewSQLiteStore(dsn string, opts ...SessionStoreOption) (*SQLiteStore, error) {
	cfg := &StoreConfig{}
	cfg.apply(opts)
	cfg.DSN = dsn
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, core.NewError(core.KindMemory, "sqlite open: "+err.Error(), err)
	}
	db.SetMaxOpenConns(1) // modernc sqlite is single-writer; serialize writes
	ctx, cnl := context.WithCancel(context.Background())
	s := &SQLiteStore{db: db, ctx: ctx, cnl: cnl, table: cfg.TableName}
	if err := s.init(); err != nil {
		db.Close()
		cnl()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) init() error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id TEXT PRIMARY KEY,
		agent TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		data TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`, s.table)
	if _, err := s.db.ExecContext(s.ctx, q); err != nil {
		return core.NewError(core.KindMemory, "sqlite init: "+err.Error(), err)
	}
	return nil
}

func (s *SQLiteStore) Save(ctx context.Context, sess *Session) error {
	sess.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(sess)
	if err != nil {
		return core.NewError(core.KindMemory, "sqlite marshal: "+err.Error(), err)
	}
	q := fmt.Sprintf(`INSERT INTO %s (id, agent, user_id, data, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET agent=excluded.agent, user_id=excluded.user_id, data=excluded.data, updated_at=excluded.updated_at`, s.table)
	if _, err := s.db.ExecContext(ctx, q, sess.ID, sess.AgentName, sess.UserID, string(data), sess.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return core.NewError(core.KindMemory, "sqlite save: "+err.Error(), err)
	}
	return nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Session, error) {
	q := fmt.Sprintf(`SELECT data FROM %s WHERE id = ?`, s.table)
	var data string
	if err := s.db.QueryRowContext(ctx, q, id).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, core.NewError(core.KindMemory, "session not found: "+id)
		}
		return nil, core.NewError(core.KindMemory, "sqlite get: "+err.Error(), err)
	}
	var sess Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, core.NewError(core.KindMemory, "sqlite decode: "+err.Error(), err)
	}
	return &sess, nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table)
	if _, err := s.db.ExecContext(ctx, q, id); err != nil {
		return core.NewError(core.KindMemory, "sqlite delete: "+err.Error(), err)
	}
	return nil
}

func (s *SQLiteStore) List(ctx context.Context, agentName string) ([]*Session, error) {
	q := fmt.Sprintf(`SELECT data FROM %s`, s.table)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, core.NewError(core.KindMemory, "sqlite list: "+err.Error(), err)
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, core.NewError(core.KindMemory, "sqlite list scan: "+err.Error(), err)
		}
		var sess Session
		if err := json.Unmarshal([]byte(data), &sess); err != nil {
			return nil, core.NewError(core.KindMemory, "sqlite list decode: "+err.Error(), err)
		}
		if agentName == "" || sess.AgentName == agentName {
			out = append(out, &sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *SQLiteStore) Close() error {
	s.cnl()
	return s.db.Close()
}

// FileName sanitises a DSN into a display name (for CLI output).
func FileName(dsn string) string {
	if dsn == "" || dsn == ":memory:" {
		return dsn
	}
	return strings.TrimSuffix(dsn, ".db") + ".db"
}
