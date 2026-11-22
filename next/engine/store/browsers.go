package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box/browser"
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

func copyFilePrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ReadV1ChromiumCookies copies a v1 Chromium Default/Cookies SQLite file (plus any adjacent WAL/SHM)
// into a private temporary directory and reads its meta version and cookie rows without modifying
// the source profile. Errors never include cookie values.
func ReadV1ChromiumCookies(ctx context.Context, dbPath string) (browser.V1CookieDB, error) {
	tmpDir, err := os.MkdirTemp("", "dyna-v1-cookies-*")
	if err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("create temp dir for v1 cookies: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chmod(tmpDir, 0700); err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("chmod temp dir: %w", err)
	}

	tmpDB := filepath.Join(tmpDir, "Cookies")
	if err := copyFilePrivate(dbPath, tmpDB); err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("copy v1 cookies database: %w", err)
	}
	hasWAL := false
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			if err := copyFilePrivate(dbPath+suffix, tmpDB+suffix); err != nil {
				return browser.V1CookieDB{}, fmt.Errorf("copy v1 cookies%s: %w", suffix, err)
			}
			if suffix == "-wal" {
				hasWAL = true
			}
		}
	}

	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(tmpDB)}
	if !hasWAL {
		u.RawQuery = "mode=ro"
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("open v1 cookies database: %w", err)
	}
	defer db.Close()

	var result browser.V1CookieDB
	var verStr string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'version'`).Scan(&verStr); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(verStr)); err == nil {
			result.Version = v
		}
	}

	cols := map[string]bool{}
	colRows, err := db.QueryContext(ctx, `PRAGMA table_info(cookies)`)
	if err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("inspect v1 cookies schema: %w", err)
	}
	for colRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := colRows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			colRows.Close()
			return browser.V1CookieDB{}, fmt.Errorf("scan v1 cookies schema: %w", err)
		}
		cols[name] = true
	}
	if err := colRows.Err(); err != nil {
		colRows.Close()
		return browser.V1CookieDB{}, fmt.Errorf("read v1 cookies schema: %w", err)
	}
	colRows.Close()
	for _, req := range []string{"host_key", "name", "value", "encrypted_value", "path", "expires_utc", "is_secure", "is_httponly"} {
		if !cols[req] {
			return browser.V1CookieDB{}, fmt.Errorf("v1 cookies table missing required column %s", req)
		}
	}

	colOr := func(name, fallback string) string {
		if cols[name] {
			return name
		}
		return fallback
	}
	query := fmt.Sprintf(
		`SELECT host_key, %s, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly, %s, %s, %s, %s, %s, %s, %s FROM cookies`,
		colOr("top_frame_site_key", "''"),
		colOr("is_persistent", "CASE WHEN expires_utc > 0 THEN 1 ELSE 0 END"),
		colOr("has_expires", "CASE WHEN expires_utc > 0 THEN 1 ELSE 0 END"),
		colOr("priority", "1"),
		colOr("samesite", "-1"),
		colOr("source_scheme", "0"),
		colOr("source_port", "-1"),
		colOr("has_cross_site_ancestor", "0"),
	)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return browser.V1CookieDB{}, fmt.Errorf("query v1 cookies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row browser.V1CookieRow
		var isSecure, isHTTPOnly, isPersistent, hasExpires, hasCrossSite int
		if err := rows.Scan(
			&row.HostKey,
			&row.TopFrameSiteKey,
			&row.Name,
			&row.Value,
			&row.EncryptedValue,
			&row.Path,
			&row.ExpiresUTC,
			&isSecure,
			&isHTTPOnly,
			&isPersistent,
			&hasExpires,
			&row.Priority,
			&row.SameSite,
			&row.SourceScheme,
			&row.SourcePort,
			&hasCrossSite,
		); err != nil {
			return browser.V1CookieDB{}, errors.New("scan v1 cookie row failed")
		}
		row.IsSecure = isSecure != 0
		row.IsHTTPOnly = isHTTPOnly != 0
		row.IsPersistent = isPersistent != 0
		row.HasExpires = hasExpires != 0
		row.HasCrossSiteAncestor = hasCrossSite != 0
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return browser.V1CookieDB{}, errors.New("iterate v1 cookie rows failed")
	}
	return result, nil
}
