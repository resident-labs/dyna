package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var (
	// ErrBrowserExists reports a duplicate shared browser profile name.
	ErrBrowserExists = errors.New("browser profile already exists")
	// ErrBrowserLeased reports that another box currently holds the profile lease.
	ErrBrowserLeased = errors.New("browser profile is in use")
	// ErrBrowserInUse reports that open rooms still reference the browser profile.
	ErrBrowserInUse = errors.New("browser profile is in use by rooms")
)

// BrowserLeaseRow is a row of browser_leases enriched with session and room names when available.
type BrowserLeaseRow struct {
	Profile     string
	Box         contract.BoxID
	Session     contract.SessionID
	SessionName string
	Room        contract.RoomID
	RoomName    string
	Generation  int64
	Since       time.Time
}

// BrowserCreate registers a new named shared browser profile.
func (s *Store) BrowserCreate(ctx context.Context, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM browsers WHERE name=?", name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return ErrBrowserExists
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO browsers(name,generation) VALUES(?,1)", name)
		return err
	})
}

// BrowserList returns every shared browser profile with its rooms and active lease.
func (s *Store) BrowserList(ctx context.Context) ([]contract.BrowserSummary, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT b.name, b.generation,
		COALESCE(l.box,''), COALESCE(l.session,''), COALESCE(l.generation,0), COALESCE(l.since,0),
		COALESCE(s.name,''), COALESCE(s.room,''), COALESCE(r.name,'')
		FROM browsers b
		LEFT JOIN browser_leases l ON l.profile=b.name
		LEFT JOIN sessions s ON s.id=l.session
		LEFT JOIN rooms r ON r.id=s.room
		ORDER BY b.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.BrowserSummary{}
	byName := map[string]int{}
	for rows.Next() {
		var item contract.BrowserSummary
		var boxID, sessionID, sessionName, roomID, roomName string
		var leaseGen, since int64
		if err := rows.Scan(&item.Name, &item.Generation, &boxID, &sessionID, &leaseGen, &since, &sessionName, &roomID, &roomName); err != nil {
			return nil, err
		}
		if boxID != "" {
			item.Lease = &contract.BrowserLease{
				Box:         contract.BoxID(boxID),
				Session:     contract.SessionID(sessionID),
				SessionName: sessionName,
				Room:        contract.RoomID(roomID),
				RoomName:    roomName,
				Generation:  leaseGen,
				Since:       time.UnixMilli(since).UTC(),
			}
		}
		byName[item.Name] = len(out)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	roomRows, err := s.Read().QueryContext(ctx, "SELECT browser, name FROM rooms WHERE state='open' AND browser!='' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer roomRows.Close()
	for roomRows.Next() {
		var browserName, roomName string
		if err := roomRows.Scan(&browserName, &roomName); err != nil {
			return nil, err
		}
		if idx, ok := byName[browserName]; ok {
			out[idx].Rooms = append(out[idx].Rooms, roomName)
		}
	}
	return out, roomRows.Err()
}

// BrowserRemove deletes a profile if it exists, is not leased, and is not named by any open room.
func (s *Store) BrowserRemove(ctx context.Context, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var gen int64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM browsers WHERE name=?", name).Scan(&gen); err != nil {
			return err
		}
		var boxID, sessionID string
		err := tx.QueryRowContext(ctx, "SELECT box, session FROM browser_leases WHERE profile=?", name).Scan(&boxID, &sessionID)
		if err == nil {
			return fmt.Errorf("%w: held by session %s (box %s); stop its computer or run dyna browser release %s first", ErrBrowserLeased, sessionID, boxID, name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rooms, err := listIDs[string](ctx, tx, "SELECT name FROM rooms WHERE state='open' AND browser=? ORDER BY name", name)
		if err != nil {
			return err
		}
		if len(rooms) > 0 {
			return fmt.Errorf("%w: %s; run dyna room browser ROOM none first", ErrBrowserInUse, strings.Join(rooms, ", "))
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM browsers WHERE name=?", name)
		return err
	})
}

// SetRoomBrowser sets an open room's shared browser profile, or clears it when name is empty.
func (s *Store) SetRoomBrowser(ctx context.Context, room contract.RoomID, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if name != "" {
			var gen int64
			if err := tx.QueryRowContext(ctx, "SELECT generation FROM browsers WHERE name=?", name).Scan(&gen); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET browser=? WHERE id=? AND state='open'", name, room)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return ErrRoomNotFound
		}
		return err
	})
}

// RoomBrowser returns the room's shared browser profile name and the names of other open rooms sharing it.
func (s *Store) RoomBrowser(ctx context.Context, room contract.RoomID) (string, []string, error) {
	var name string
	err := s.Read().QueryRowContext(ctx, "SELECT browser FROM rooms WHERE id=? AND state='open'", room).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrRoomNotFound
	}
	if err != nil || name == "" {
		return name, nil, err
	}
	others, err := listIDs[string](ctx, s.Read(), "SELECT name FROM rooms WHERE state='open' AND browser=? AND id!=? ORDER BY name", name, room)
	return name, others, err
}

// BrowserCheckout acquires the lease on the room's shared browser profile for boxID/sessionID.
// When the room has no shared browser profile, it returns empty profile name and nil error.
// When another box holds the lease, it returns the profile name, the current holder, and ErrBrowserLeased.
// When the same box already holds the lease, it returns the existing generation idempotently.
func (s *Store) BrowserCheckout(ctx context.Context, boxID contract.BoxID, sessionID contract.SessionID, room contract.RoomID, now time.Time) (string, int64, *BrowserLeaseRow, error) {
	var profile string
	var gen int64
	var holder *BrowserLeaseRow
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT browser FROM rooms WHERE id=? AND state='open'", room).Scan(&profile); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return err
		}
		if profile == "" {
			return nil
		}
		var currentGen int64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM browsers WHERE name=?", profile).Scan(&currentGen); err != nil {
			return err
		}
		var existing BrowserLeaseRow
		var since int64
		err := tx.QueryRowContext(ctx, `SELECT l.profile, l.box, l.session, l.generation, l.since,
			COALESCE(s.name,''), COALESCE(s.room,''), COALESCE(r.name,'')
			FROM browser_leases l
			LEFT JOIN sessions s ON s.id=l.session
			LEFT JOIN rooms r ON r.id=s.room
			WHERE l.profile=?`, profile).Scan(&existing.Profile, &existing.Box, &existing.Session, &existing.Generation, &since, &existing.SessionName, &existing.Room, &existing.RoomName)
		if err == nil {
			existing.Since = time.UnixMilli(since).UTC()
			if existing.Box == boxID {
				gen = existing.Generation
				return nil
			}
			holder = &existing
			return ErrBrowserLeased
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		gen = currentGen + 1
		if _, err := tx.ExecContext(ctx, "UPDATE browsers SET generation=? WHERE name=?", gen, profile); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO browser_leases(profile,box,session,generation,since) VALUES(?,?,?,?,?)", profile, boxID, sessionID, gen, now.UnixMilli())
		return err
	})
	return profile, gen, holder, err
}

// BrowserLeaseMatches reports whether boxID still holds the lease on profile at generation.
func (s *Store) BrowserLeaseMatches(ctx context.Context, profile string, boxID contract.BoxID, generation int64) (bool, error) {
	var matches bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM browser_leases WHERE profile=? AND box=? AND generation=?)", profile, boxID, generation).Scan(&matches)
	return matches, err
}

// BrowserFinishCheckin releases the lease only if profile, boxID, and generation still match.
func (s *Store) BrowserFinishCheckin(ctx context.Context, profile string, boxID contract.BoxID, generation int64) (bool, error) {
	var ok bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM browser_leases WHERE profile=? AND box=? AND generation=?", profile, boxID, generation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		ok = n > 0
		return err
	})
	return ok, err
}

// BrowserRelease forcibly releases any lease on profile and bumps its generation so any late checkin is refused.
func (s *Store) BrowserRelease(ctx context.Context, name string) (released bool, generation int64, err error) {
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var cur int64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM browsers WHERE name=?", name).Scan(&cur); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM browser_leases WHERE profile=?", name)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		released = n > 0
		generation = cur + 1
		_, err = tx.ExecContext(ctx, "UPDATE browsers SET generation=? WHERE name=?", generation, name)
		return err
	})
	return released, generation, err
}

// BoxHoldsBrowserLease reports whether boxID currently holds any browser profile lease.
func (s *Store) BoxHoldsBrowserLease(ctx context.Context, boxID contract.BoxID) (bool, error) {
	var holds bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM browser_leases WHERE box=?)", boxID).Scan(&holds)
	return holds, err
}

// BrowserLeases returns all currently active browser leases.
func (s *Store) BrowserLeases(ctx context.Context) ([]BrowserLeaseRow, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT l.profile, l.box, l.session, l.generation, l.since,
		COALESCE(s.name,''), COALESCE(s.room,''), COALESCE(r.name,'')
		FROM browser_leases l
		LEFT JOIN sessions s ON s.id=l.session
		LEFT JOIN rooms r ON r.id=s.room
		ORDER BY l.profile`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BrowserLeaseRow
	for rows.Next() {
		var row BrowserLeaseRow
		var since int64
		if err := rows.Scan(&row.Profile, &row.Box, &row.Session, &row.Generation, &since, &row.SessionName, &row.Room, &row.RoomName); err != nil {
			return nil, err
		}
		row.Since = time.UnixMilli(since).UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}
