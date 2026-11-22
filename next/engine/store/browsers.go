package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

var (
	// ErrBrowserExists reports a duplicate shared browser profile name.
	ErrBrowserExists = errors.New("browser profile already exists")
	// ErrBrowserInUse reports that open rooms still reference the browser profile.
	ErrBrowserInUse = errors.New("browser profile is in use by rooms")
)

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
		_, err := tx.ExecContext(ctx, "INSERT INTO browsers(name) VALUES(?)", name)
		return err
	})
}

// BrowserList returns every shared browser profile with the open rooms naming it.
func (s *Store) BrowserList(ctx context.Context) ([]contract.BrowserSummary, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT name FROM browsers ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.BrowserSummary{}
	byName := map[string]int{}
	for rows.Next() {
		var item contract.BrowserSummary
		if err := rows.Scan(&item.Name); err != nil {
			return nil, err
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

// BrowserRemove deletes a profile if it exists and is not named by any open room.
func (s *Store) BrowserRemove(ctx context.Context, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var exists string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM browsers WHERE name=?", name).Scan(&exists); err != nil {
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
			var exists string
			if err := tx.QueryRowContext(ctx, "SELECT name FROM browsers WHERE name=?", name).Scan(&exists); err != nil {
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
