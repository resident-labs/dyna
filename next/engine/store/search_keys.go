package store

import (
	"context"
	"database/sql"
)

// SearchKey reads a search-only key. It is never returned to API clients.
func (s *Store) SearchKey(ctx context.Context, provider string) (string, error) {
	var key string
	err := s.read.QueryRowContext(ctx, "SELECT key FROM search_keys WHERE provider=?", provider).Scan(&key)
	return key, err
}

func (s *Store) SetSearchKey(ctx context.Context, provider, key string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO search_keys(provider,key) VALUES(?,?) ON CONFLICT(provider) DO UPDATE SET key=excluded.key", provider, key)
		return err
	})
}

func (s *Store) RemoveSearchKey(ctx context.Context, provider string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM search_keys WHERE provider=?", provider)
		return err
	})
}
