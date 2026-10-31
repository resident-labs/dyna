package store

import (
	"context"
	"database/sql"
)

// ProxyAuth is an opaque credential record owned by the proxy.
type ProxyAuth struct {
	ID        string
	Provider  string
	Record    []byte
	UpdatedAt int64
}

func (s *Store) SaveProxyAuth(ctx context.Context, auth ProxyAuth, create bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if create {
			_, err := tx.ExecContext(ctx, `INSERT INTO proxy_auths(id,provider,record,updated_at) VALUES(?,?,?,?)`, auth.ID, auth.Provider, auth.Record, auth.UpdatedAt)
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE proxy_auths SET provider=?,record=?,updated_at=? WHERE id=?`, auth.Provider, auth.Record, auth.UpdatedAt, auth.ID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

func (s *Store) ListProxyAuths(ctx context.Context) ([]ProxyAuth, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,provider,record,updated_at FROM proxy_auths ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var auths []ProxyAuth
	for rows.Next() {
		var auth ProxyAuth
		if err := rows.Scan(&auth.ID, &auth.Provider, &auth.Record, &auth.UpdatedAt); err != nil {
			return nil, err
		}
		auths = append(auths, auth)
	}
	return auths, rows.Err()
}

// PruneUnboundProxyKeys reconciles interrupted key creation before proxy startup.
func (s *Store) PruneUnboundProxyKeys(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM proxy_auths WHERE id GLOB 'dyna-key-[0-9a-f]*' AND NOT EXISTS (SELECT 1 FROM accounts WHERE proxy_id=proxy_auths.id)")
		return err
	})
}

func (s *Store) DeleteProxyAuth(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM proxy_auths WHERE id=?", id)
		return err
	})
}
