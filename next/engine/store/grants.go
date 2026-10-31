package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"time"
)

func (s *Store) InsertGrant(tx *sql.Tx, g contract.Grant) error {
	var expires *int64
	if !g.Expires.IsZero() {
		v := g.Expires.UnixMilli()
		expires = &v
	}
	_, err := tx.Exec("INSERT INTO grants(id,kind,room,to_kind,to_id,mode,expires_at,by_kind,by_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", g.ID, g.Kind, g.Room, g.To.Kind, g.To.Session, g.Mode, expires, g.By.Kind, g.By.Session, g.Created.UnixMilli())
	if err != nil {
		return err
	}
	for _, h := range g.Hosts {
		if _, err = tx.Exec("INSERT INTO grant_hosts VALUES(?,?)", g.ID, h); err != nil {
			return err
		}
	}
	for i, c := range g.Commands {
		if _, err = tx.Exec("INSERT INTO grant_commands VALUES(?,?,?,?)", g.ID, i, c.Program, c.Prefix); err != nil {
			return err
		}
		for j, a := range c.Args {
			if _, err = tx.Exec("INSERT INTO grant_command_args VALUES(?,?,?,?)", g.ID, i, j, a); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ListActiveGrants(ctx context.Context, room contract.RoomID, who contract.Principal, now time.Time) ([]contract.Grant, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT g.id,g.mode,g.expires_at,g.by_kind,g.by_id,g.created_at,h.host,c.idx,c.program,c.prefix,a.pos,a.arg
 FROM grants g JOIN grant_hosts h ON h.grant_id=g.id JOIN grant_commands c ON c.grant_id=g.id
 LEFT JOIN grant_command_args a ON a.grant_id=c.grant_id AND a.idx=c.idx
 WHERE g.room=? AND g.to_kind=? AND g.to_id=? AND g.kind='hostexec' AND g.revoked_at IS NULL AND g.expired_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>?)
 ORDER BY g.id,c.idx,a.pos,h.host`, room, who.Kind, who.Session, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contract.Grant
	var last contract.GrantID
	var firstHost contract.HostName
	// Rows are hosts × commands × args: take hosts from command 0, arg 0, and args from the first host.
	for rows.Next() {
		var id contract.GrantID
		var mode contract.GrantMode
		var expiry sql.NullInt64
		var by contract.Principal
		var created int64
		var host contract.HostName
		var idx int
		var program string
		var prefix bool
		var pos sql.NullInt64
		var arg sql.NullString
		if err = rows.Scan(&id, &mode, &expiry, &by.Kind, &by.Session, &created, &host, &idx, &program, &prefix, &pos, &arg); err != nil {
			return nil, err
		}
		if id != last {
			out = append(out, contract.Grant{ID: id, Kind: contract.GrantHostExec, Room: room, To: who, Mode: mode, By: by, Created: time.UnixMilli(created).UTC()})
			if expiry.Valid {
				out[len(out)-1].Expires = time.UnixMilli(expiry.Int64).UTC()
			}
			last = id
			firstHost = host
		}
		g := &out[len(out)-1]
		if idx == 0 && (!pos.Valid || pos.Int64 == 0) {
			g.Hosts = append(g.Hosts, host)
		}
		if idx == len(g.Commands) {
			g.Commands = append(g.Commands, contract.CommandRule{Program: program, Prefix: prefix})
		}
		if host == firstHost && pos.Valid {
			g.Commands[idx].Args = append(g.Commands[idx].Args, arg.String)
		}
	}
	return out, rows.Err()
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
		return nil
	})
}

// H2 must stop pending and running commands after a revoke.

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
