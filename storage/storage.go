// Package storage is the public API for session stores.
// It forwards to the implementation in ernest/internal/storage.
package storage

import internal "github.com/nemo715/Ernest/internal/storage"

type (
	SessionStore         = internal.SessionStore
	InMemoryStore        = internal.InMemoryStore
	SQLiteStore          = internal.SQLiteStore
	SessionStoreOption   = internal.SessionStoreOption
)

// NewInMemoryStore builds a store that keeps sessions in memory.
func NewInMemoryStore() *InMemoryStore {
	return internal.NewInMemoryStore()
}

// NewSQLiteStore builds a persistent store at the given DSN.
func NewSQLiteStore(dsn string, opts ...SessionStoreOption) (*SQLiteStore, error) {
	return internal.NewSQLiteStore(dsn, opts...)
}
