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
	return s.Write(ctx, func(tx *sql.Tx) error { return s.InsertGrant(tx, g) })
}

func (s *Store) InsertGrant(tx *sql.Tx, g contract.Grant) error {
	hosts, err := json.Marshal(g.Hosts)
	if err != nil {
		return err
	}
	var expires *int64
	if !g.Expires.IsZero() {
		v := g.Expires.UnixMilli()
		expires = &v
	}
	_, err = tx.Exec("INSERT INTO grants(id,kind,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?)", g.ID, g.Kind, g.Room, g.To.Kind, g.To.Session, hosts, expires, g.By.Kind, g.By.Session, g.Created.UnixMilli(), g.Reason)
	return err
}

func (s *Store) ListActiveGrants(ctx context.Context, room contract.RoomID, who contract.Principal, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason FROM grants WHERE room=? AND to_kind=? AND to_id=? AND kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND state!='closing' AND closed_at IS NULL) ORDER BY id`, room, who.Kind, who.Session)
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

func (s *Store) ListHostexecGrants(ctx context.Context, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason FROM grants WHERE kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND state!='closing' AND closed_at IS NULL) ORDER BY created_at DESC,id DESC`)
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
