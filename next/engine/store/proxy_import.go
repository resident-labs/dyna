package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
)

type ImportedProxyAuth struct {
	Auth      ProxyAuth
	AccountID string
	Provider  string
	Label     string
}

// ImportProxyAuths commits the legacy import and marker in the same writer transaction.
func (s *Store) ImportProxyAuths(ctx context.Context, records []ImportedProxyAuth) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var marker string
		err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='proxy_auth_imported'").Scan(&marker)
		if err == nil {
			for _, record := range records {
				var provider string
				var raw []byte
				if err := tx.QueryRowContext(ctx, "SELECT provider,record FROM proxy_auths WHERE id=?", record.Auth.ID).Scan(&provider, &raw); err != nil {
					return err
				}
				if provider != record.Auth.Provider || !bytes.Equal(raw, record.Auth.Record) {
					return errors.New("legacy credential changed")
				}
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		for _, record := range records {
			a := record.Auth
			if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_auths(id,provider,record,updated_at) VALUES(?,?,?,?)", a.ID, a.Provider, a.Record, a.UpdatedAt); err != nil {
				return err
			}
			if record.AccountID != "" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) VALUES(?,?,?,?)", record.AccountID, a.ID, record.Provider, record.Label); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('proxy_auth_imported','1')")
		return err
	})
}
