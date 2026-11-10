package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// CreateGrant inserts a grant in its own transaction.
func (s *Store) CreateGrant(ctx context.Context, g contract.Grant) error {
	return s.CreateGrants(ctx, []contract.Grant{g})
}

func (s *Store) InsertGrant(tx *sql.Tx, g contract.Grant) error {
	hosts, _ := json.Marshal(g.Hosts)
	var expires *int64
	if !g.Expires.IsZero() {
		v := g.Expires.UnixMilli()
		expires = &v
	}
	_, err := tx.Exec("INSERT INTO grants(id,kind,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?)", g.ID, g.Kind, g.Room, g.To.Kind, g.To.Session, hosts, expires, g.By.Kind, g.By.Session, g.Created.UnixMilli(), g.Reason)
	return err
}

func (s *Store) ListActiveGrants(ctx context.Context, room contract.RoomID, who contract.Principal, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason FROM grants WHERE room=? AND to_kind=? AND to_id=? AND kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND (expires_at IS NULL OR expires_at>?) AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND state!='closing' AND closed_at IS NULL) ORDER BY id`, room, who.Kind, who.Session, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

func (s *Store) ListHostexecGrants(ctx context.Context, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason FROM grants WHERE kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND (expires_at IS NULL OR expires_at>?) AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND state!='closing' AND closed_at IS NULL) ORDER BY created_at DESC,id DESC`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

func scanGrants(rows *sql.Rows) ([]contract.Grant, error) {
	var err error
	defer rows.Close()
	grants := []contract.Grant{}
	for rows.Next() {
		g := contract.Grant{Kind: contract.GrantHostExec}
		var hosts []byte
		var expires sql.NullInt64
		var created int64
		if err = rows.Scan(&g.ID, &g.Room, &g.To.Kind, &g.To.Session, &hosts, &expires, &g.By.Kind, &g.By.Session, &created, &g.Reason); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(hosts, &g.Hosts); err != nil {
			return nil, err
		}
		g.Created = time.UnixMilli(created).UTC()
		if expires.Valid {
			g.Expires = time.UnixMilli(expires.Int64).UTC()
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// CreateGrants inserts a path of grants atomically.
func (s *Store) CreateGrants(ctx context.Context, grants []contract.Grant) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, grant := range grants {
			if err := s.InsertGrant(tx, grant); err != nil {
				return err
			}
		}
		return nil
	})
}
