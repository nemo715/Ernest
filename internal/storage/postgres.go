package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ernest/internal/core"
)

// PostgresStore persists sessions to PostgreSQL via pgx (pure Go).
// The table is created automatically with a JSONB payload column.
type PostgresStore struct {
	pool  *pgxpool.Pool
	table string
}

// NewPostgresStore connects to PostgreSQL. dsn is a pgx URL, e.g.
// "postgres://user:pass@localhost:5432/ernest".
func NewPostgresStore(dsn string, opts ...SessionStoreOption) (*PostgresStore, error) {
	cfg := &StoreConfig{}
	cfg.apply(opts)
	cfg.DSN = dsn
	ctx, cnl := context.WithTimeout(context.Background(), 15*time.Second)
	defer cnl()
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, core.NewError(core.KindMemory, "postgres connect: "+err.Error(), err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, core.NewError(core.KindMemory, "postgres ping: "+err.Error(), err)
	}
	s := &PostgresStore{pool: pool, table: cfg.TableName}
	if err := s.init(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) init(ctx context.Context) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id TEXT PRIMARY KEY,
		agent TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		data JSONB NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`, s.table)
	if _, err := s.pool.Exec(ctx, q); err != nil {
		return core.NewError(core.KindMemory, "postgres init: "+err.Error(), err)
	}
	return nil
}

func (s *PostgresStore) Save(ctx context.Context, sess *Session) error {
	sess.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(sess)
	if err != nil {
		return core.NewError(core.KindMemory, "postgres marshal: "+err.Error(), err)
	}
	q := fmt.Sprintf(`INSERT INTO %s (id, agent, user_id, data, updated_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(id) DO UPDATE SET agent=excluded.agent, user_id=excluded.user_id, data=excluded.data, updated_at=excluded.updated_at`, s.table)
	if _, err := s.pool.Exec(ctx, q, sess.ID, sess.AgentName, sess.UserID, data, sess.UpdatedAt); err != nil {
		return core.NewError(core.KindMemory, "postgres save: "+err.Error(), err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (*Session, error) {
	q := fmt.Sprintf(`SELECT data FROM %s WHERE id = $1`, s.table)
	var data []byte
	if err := s.pool.QueryRow(ctx, q, id).Scan(&data); err != nil {
		if err == pgx.ErrNoRows {
			return nil, core.NewError(core.KindMemory, "session not found: "+id)
		}
		return nil, core.NewError(core.KindMemory, "postgres get: "+err.Error(), err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, core.NewError(core.KindMemory, "postgres decode: "+err.Error(), err)
	}
	return &sess, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.table)
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return core.NewError(core.KindMemory, "postgres delete: "+err.Error(), err)
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context, agentName string) ([]*Session, error) {
	q := fmt.Sprintf(`SELECT data FROM %s`, s.table)
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, core.NewError(core.KindMemory, "postgres list: "+err.Error(), err)
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, core.NewError(core.KindMemory, "postgres list scan: "+err.Error(), err)
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			return nil, core.NewError(core.KindMemory, "postgres list decode: "+err.Error(), err)
		}
		if agentName == "" || sess.AgentName == agentName {
			out = append(out, &sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}
