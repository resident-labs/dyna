package subagent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/turnlock"
)

const promptStoreFormat = 3

type promptLaunch struct {
	process  callerid.Process
	format   int
	launcher string
}
type promptRunner struct {
	id        string
	pid, turn int
}
type promptCutoverProbes struct {
	launches   func() ([]promptLaunch, error)
	candidates func() ([]promptRunner, error)
	process    func(int) (callerid.Process, error)
	matches    func(int, string, int) (bool, bool)
	launch     func(callerid.Process) (callerid.Launched, bool, error)
}

// preflightPromptCutover checks whether known writers can use format 3.
// It assumes old external CLI writers are quiescent. Inspection cannot stop
// another old-format process from starting afterward. P2022c must call this
// before activating format 3; this port does not activate the guard.
func preflightPromptCutover(db *sql.DB, sessions string) error {
	launches, err := callerid.OpenStore(filepath.Dir(sessions))
	if err != nil {
		return err
	}
	return checkPromptCutover(promptCutoverProbes{
		launches:   func() ([]promptLaunch, error) { return promptLaunches(db) },
		candidates: func() ([]promptRunner, error) { return promptRunners(db, sessions) },
		process:    callerid.ProcessOf, matches: turnlock.RunnerMatches, launch: launches.ProcessLaunch,
	})
}

func checkPromptCutover(p promptCutoverProbes) error {
	launches, err := p.launches()
	if err != nil {
		return fmt.Errorf("read prompt writer launches: %w", err)
	}
	for _, row := range launches {
		switch row.launcher {
		case "turn", "consolidate", "transfer", "resume-room":
		default:
			continue
		}
		if row.process.PID <= 0 || row.process.Start == 0 {
			return fmt.Errorf("invalid %s launch identity: %+v", row.launcher, row.process)
		}
		current, err := p.process(row.process.PID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("verify %s pid %d: %w", row.launcher, row.process.PID, err)
		}
		if current.PID != row.process.PID || current.Start == 0 {
			return fmt.Errorf("unknown process identity for pid %d", row.process.PID)
		}
		if current.Start != row.process.Start {
			continue
		}
		if row.format < promptStoreFormat {
			return fmt.Errorf("live %s pid %d uses format %d; need %d", row.launcher, current.PID, row.format, promptStoreFormat)
		}
	}
	candidates, err := p.candidates()
	if err != nil {
		return fmt.Errorf("read prompt runner candidates: %w", err)
	}
	for _, c := range candidates {
		process, err := p.process(c.pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("verify runner pid %d of %s: %w", c.pid, c.id, err)
		}
		if process.PID != c.pid || process.Start == 0 {
			return fmt.Errorf("unknown runner identity for pid %d", c.pid)
		}
		matches, known := p.matches(c.pid, c.id, c.turn)
		if !known {
			return fmt.Errorf("cannot verify runner pid %d of %s", c.pid, c.id)
		}
		if !matches {
			continue
		}
		// Pin the command-line observation to the same process start identity.
		current, err := p.process(c.pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || current != process {
			return fmt.Errorf("runner identity changed during inspection of pid %d", c.pid)
		}
		launch, found, err := p.launch(process)
		if err != nil {
			return fmt.Errorf("read runner launch for pid %d: %w", c.pid, err)
		}
		if !found || launch.Kind != callerid.Session || launch.Session != c.id || launch.Format < promptStoreFormat {
			return fmt.Errorf("runner pid %d of %s lacks a verified format %d launch", c.pid, c.id, promptStoreFormat)
		}
	}
	return nil
}

func promptLaunches(db *sql.DB) ([]promptLaunch, error) {
	rows, err := db.Query("SELECT pid, start, format, launcher FROM caller_records")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []promptLaunch
	for rows.Next() {
		var row promptLaunch
		if err := rows.Scan(&row.process.PID, &row.process.Start, &row.format, &row.launcher); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// promptRunners reads canonical session records and retained runner gates.
// TODO: legacy/shim remove later — gate.json remains a runner identity source.
func promptRunners(db *sql.DB, sessions string) ([]promptRunner, error) {
	var out []promptRunner
	seen := map[promptRunner]bool{}
	add := func(id string, raw []byte, field string) error {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return fmt.Errorf("invalid runner source for %s", id)
		}
		var pid, turn int
		if value, ok := fields["pid"]; ok {
			if string(value) == "null" {
				return fmt.Errorf("invalid runner pid for %s", id)
			}
			if err := json.Unmarshal(value, &pid); err != nil {
				return err
			}
		}
		if value, ok := fields[field]; ok {
			if err := json.Unmarshal(value, &turn); err != nil {
				return err
			}
		}
		if pid < 0 || (pid > 0 && turn <= 0) {
			return fmt.Errorf("invalid runner pid or turn for %s", id)
		}
		c := promptRunner{id, pid, turn}
		if pid > 0 && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
		return nil
	}
	rows, err := db.Query("SELECT id, meta FROM session_records")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err := add(id, raw, "turns"); err != nil {
			rows.Close()
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !sessionlife.IsSessionID(entry.Name()) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("session folder %s is a symlink", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(sessions, entry.Name(), turnlock.GateFile)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("runner gate %s is not a regular file", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := add(entry.Name(), raw, "turn"); err != nil {
			return nil, fmt.Errorf("gate %s: %w", path, err)
		}
	}
	return out, nil
}
