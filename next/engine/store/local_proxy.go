package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const localProxyKey = "local_proxy_key"

// LocalProxyKey reads the owner's proxy key. It is empty until first use.
func (s *Store) LocalProxyKey(ctx context.Context) (string, error) {
	var key string
	err := s.read.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", localProxyKey).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// SetLocalProxyKey stores key if there is none, or always when replace is set, and returns the key now stored.
func (s *Store) SetLocalProxyKey(ctx context.Context, key string, replace bool) (string, error) {
	statement := "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING"
	if replace {
		statement = "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value"
	}
	var stored string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, statement, localProxyKey, key); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", localProxyKey).Scan(&stored)
	})
	return stored, err
}

// AddLocalUsage adds one finished request to its model's totals.
func (s *Store) AddLocalUsage(ctx context.Context, u contract.ProxyUsage) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO local_usage(model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,last_at) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(model) DO UPDATE SET requests=requests+excluded.requests,input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,last_at=excluded.last_at`,
			u.Model, u.Requests, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.LastAt.UnixMilli())
		return err
	})
}

// LocalUsage lists the totals by model, most recently used first.
func (s *Store) LocalUsage(ctx context.Context) ([]contract.ProxyUsage, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,last_at FROM local_usage ORDER BY last_at DESC,model")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := []contract.ProxyUsage{}
	for rows.Next() {
		var u contract.ProxyUsage
		var at int64
		if err := rows.Scan(&u.Model, &u.Requests, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &at); err != nil {
			return nil, err
		}
		u.LastAt = time.UnixMilli(at).UTC()
		usage = append(usage, u)
	}
	return usage, rows.Err()
}
