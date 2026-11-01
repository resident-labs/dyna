package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract/private"
)

// ProxyAuth is an opaque credential record owned by the proxy.
type ProxyAuth = private.ProxyAuth

func (s *Store) SaveProxyAuth(ctx context.Context, auth ProxyAuth, create bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if create {
			result, err := tx.ExecContext(ctx, `INSERT INTO proxy_auths(id,provider,record,updated_at) SELECT ?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE proxy_id=?)`, auth.ID, auth.Provider, auth.Record, auth.UpdatedAt, auth.ID)
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
	rows, err := s.Read().QueryContext(ctx, "SELECT p.id,p.provider,p.record,p.updated_at,EXISTS (SELECT 1 FROM accounts a WHERE a.proxy_id=p.id) FROM proxy_auths p ORDER BY p.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var auths []ProxyAuth
	for rows.Next() {
		var auth ProxyAuth
		if err := rows.Scan(&auth.ID, &auth.Provider, &auth.Record, &auth.UpdatedAt, &auth.Bound); err != nil {
			return nil, err
		}
		auths = append(auths, auth)
	}
	return auths, rows.Err()
}

// PruneUnboundProxyAuths reconciles interrupted account creation before proxy startup.
func (s *Store) PruneUnboundProxyAuths(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM proxy_auths WHERE (provider IN ('claude','codex','kimi','xai','meta') OR id GLOB 'dyna-key-[0-9a-f]*') AND NOT EXISTS (SELECT 1 FROM accounts WHERE proxy_id=proxy_auths.id)")
		return err
	})
}

func (s *Store) DeleteProxyAuth(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM proxy_auths WHERE id=?", id)
		return err
	})
}
