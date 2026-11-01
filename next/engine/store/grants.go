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
	return s.listActiveGrants(ctx, room, who, now)
}

func (s *Store) ListHostexecGrants(ctx context.Context, now time.Time) ([]contract.Grant, error) {
	return s.listActiveGrants(ctx, "", contract.Principal{}, now)
}

func (s *Store) listActiveGrants(ctx context.Context, room contract.RoomID, who contract.Principal, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,room,to_kind,to_id,hosts,expires_at,by_kind,by_id,created_at,reason FROM grants WHERE (?='' OR (room=? AND to_kind=? AND to_id=?)) AND kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at DESC,id DESC`, room, room, who.Kind, who.Session, now.UnixMilli())
	if err != nil {
		return nil, err
	}
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

func (s *Store) RevokeGrant(ctx context.Context, id contract.GrantID, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.Exec("UPDATE grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now.UnixMilli(), id)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		_, err = tx.Exec("UPDATE hostexec_runs SET stopped='revoked' WHERE grant_id=? AND ended_at IS NULL", id)
		return err
	})
}

// ExpireGrants marks due grants and appends each event in the same transaction.
func (s *Store) ExpireGrants(ctx context.Context, now time.Time, event func(contract.RoomID, contract.GrantID) (contract.Event, error)) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("UPDATE grants SET expired_at=? WHERE expired_at IS NULL AND revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at<=? RETURNING id,room", now.UnixMilli(), now.UnixMilli())
		if err != nil {
			return err
		}
		type due struct {
			id   contract.GrantID
			room contract.RoomID
		}
		var found []due
		for rows.Next() {
			var d due
			if err = rows.Scan(&d.id, &d.room); err != nil {
				rows.Close()
				return err
			}
			found = append(found, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, d := range found {
			e, err := event(d.room, d.id)
			if err != nil {
				return err
			}
			if _, err = s.AppendEvent(tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
