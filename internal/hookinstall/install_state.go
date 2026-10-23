package hookinstall

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

type installState struct {
	Installed string
	Alerted   string
}

func (s Store) installState(path string) (installState, error) {
	db, err := s.db()
	if err != nil {
		return installState{}, err
	}
	var state installState
	err = db.QueryRow("SELECT installed_commit,alerted FROM hook_install_state WHERE state=?", path).Scan(&state.Installed, &state.Alerted)
	if err == sql.ErrNoRows {
		err = nil
	}
	return state, err
}

func (s Store) saveInstallState(path string, state installState) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(context.Background(), hub.Event{}, func(c *sql.Conn) error {
		_, err := c.ExecContext(context.Background(), "INSERT INTO hook_install_state(state,installed_commit,alerted) VALUES(?,?,?) ON CONFLICT(state) DO UPDATE SET installed_commit=excluded.installed_commit,alerted=excluded.alerted", path, state.Installed, state.Alerted)
		return err
	})
}

// TODO: legacy/shim remove later — imports installed commits and warning markers once.
func (s Store) importInstallState(c *sql.Conn) error {
	ctx := context.Background()
	if _, err := c.ExecContext(ctx, "CREATE TABLE hook_install_state(state TEXT PRIMARY KEY,installed_commit TEXT NOT NULL,alerted TEXT NOT NULL)"); err != nil {
		return err
	}
	paths := map[string]bool{DefaultConfig("", "").State: true}
	rows, err := c.QueryContext(ctx, "SELECT body FROM hook_install_jobs")
	if err != nil {
		return err
	}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		j, err := decodeJob(body)
		if err != nil {
			rows.Close()
			return err
		}
		if filepath.IsAbs(j.Config.State) && filepath.Clean(j.Config.State) == j.Config.State {
			paths[j.Config.State] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for path := range paths {
		installed, err := readInstallSource(path, true)
		if err != nil {
			return err
		}
		alerted, err := readInstallSource(filepath.Join(filepath.Dir(path), "alerted"), false)
		if err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "INSERT INTO hook_install_state(state,installed_commit,alerted) VALUES(?,?,?)", path, installed, alerted); err != nil {
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — validates bounded retained installation files.
func readInstallSource(path string, commit bool) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return "", fmt.Errorf("invalid install state source %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, actual) {
		return "", fmt.Errorf("install state source changed: %s", path)
	}
	body, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return "", err
	}
	if len(body) > 1024 {
		return "", fmt.Errorf("install state source too large: %s", path)
	}
	value := strings.TrimSpace(string(body))
	if commit && !shaPattern.MatchString(value) {
		return "", fmt.Errorf("invalid installed commit in %s", path)
	}
	return value, nil
}
