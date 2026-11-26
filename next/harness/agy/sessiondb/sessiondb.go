// Package sessiondb reads the records agy keeps of a conversation under its profile: the session's SQLite log (what the
// prompt's last step was), and the inbox and subagent files of its background work. It is a port of T3 Code's
// AntigravityTurnOutcome.ts, AntigravitySessionFiles.ts, AntigravityProfilePaths.ts and the readers in AntigravityChildWork.ts.
//
// It is the one package outside engine/store that imports database/sql (sql_guard_test.go names it). It only reads: the
// database opens read-only with query_only, and a record counts only when every path component below the profile exists
// with the expected type and none is a link, so another session's file under the same profile is never read. Dyna never
// reads credentials: nothing here opens a file other than these records.
package sessiondb

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	_ "modernc.org/sqlite"
)

const (
	// errorStepType is CORTEX_STEP_TYPE_ERROR_MESSAGE in the native trajectory (Outcome:15).
	errorStepType = 17
	// plannerResponseStepType is CORTEX_STEP_TYPE_PLANNER_RESPONSE: the model answered within the turn (Outcome:17).
	plannerResponseStepType = 15
	// maxErrorPayload: the harness error text leads the payload, a Go stack trace follows it (Outcome:19).
	maxErrorPayload = 16 << 10
	// MaxFileBytes bounds every record file read (ChildWork:37).
	maxFileBytes = 1 << 20
)

var (
	nativeSessionID = regexp.MustCompile(`^[\w-]+$`) // Outcome:68
	nativeID        = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// Step is the last step the prompt added to the session log.
type Step struct {
	Error        string // the payload of an error step, empty for any other
	IsError      bool
	ReachedModel bool // a planner response came within the prompt, so the model answered
}

// managedPath proves an entry is at its literal path below the canonical profile root: every component exists with the
// expected type and none is a link (T3 AntigravityProfilePaths.ts:14-37).
func managedPath(root string, segments []string, wantFile bool) (string, os.FileInfo, bool) {
	current := root
	var info os.FileInfo
	for i, seg := range segments {
		current = filepath.Join(current, seg)
		var err error
		if info, err = os.Lstat(current); err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", nil, false
		}
		if wantFile && i == len(segments)-1 {
			if !info.Mode().IsRegular() {
				return "", nil, false
			}
		} else if !info.IsDir() {
			return "", nil, false
		}
	}
	if info == nil {
		return "", nil, false
	}
	if real, err := filepath.EvalSymlinks(current); err != nil || real != current {
		return "", nil, false
	}
	return current, info, true
}

func profileRoot(profileDir string) (string, bool) {
	root, err := filepath.EvalSymlinks(profileDir)
	return root, err == nil
}

var conversations = []string{"antigravity-acp", "conversations"}

// Log is one session's log, read before a prompt so that only the steps the prompt adds are looked at (Outcome:279-354).
type Log struct {
	root     string
	segments []string
	identity os.FileInfo // the database file before the prompt; nil when the prompt may create it
	start    int64
}

// Open reads the index of the last step before a prompt. It returns a nil Log, and why, when the log is not provably this
// session's own file: the caller then relies on the typed stop reason alone, as T3 does.
func Open(profileDir, sessionID string) (*Log, string) {
	if !nativeSessionID.MatchString(sessionID) {
		return nil, ""
	}
	root, ok := profileRoot(profileDir)
	if !ok {
		return nil, "The Antigravity session log is not this session's own file."
	}
	if _, _, ok := managedPath(root, conversations, false); !ok {
		return nil, "The Antigravity session log is not this session's own file."
	}
	segments := append(append([]string{}, conversations...), sessionID+".db")
	l := &Log{root: root, segments: segments}
	path, info, ok := managedPath(root, segments, true)
	if !ok {
		// A path that exists but fails the proof is a link or other foreign entry.
		if _, err := os.Lstat(filepath.Join(append([]string{root}, segments...)...)); err == nil {
			return nil, "The Antigravity session log is not this session's own file."
		}
		l.start = -1
		return l, ""
	}
	l.identity = info
	start, found, err := readSteps(path, func(db *sql.DB) (int64, error) {
		var idx sql.NullInt64
		if err := db.QueryRow("SELECT max(idx) FROM steps").Scan(&idx); err != nil {
			return 0, err
		}
		if !idx.Valid {
			return -1, nil
		}
		return idx.Int64, nil
	})
	if err != nil {
		return nil, "Could not read the Antigravity session log before the prompt."
	}
	l.start = start
	if !found {
		l.start = -1
	}
	return l, ""
}

// Check reads the last step added since Open. It is nil when there is none or the log cannot be read: the same unlinked
// file as before the prompt, or one the prompt created. The warning says why a log could not be read.
func (l *Log) Check() (*Step, string) {
	path, info, ok := managedPath(l.root, l.segments, true)
	if !ok || (l.identity != nil && !os.SameFile(l.identity, info)) {
		return nil, ""
	}
	step, _, err := readSteps(path, func(db *sql.DB) (*Step, error) {
		var typ int64
		var payload []byte
		err := db.QueryRow(`SELECT step_type, substr(step_payload, 1, ?) FROM steps WHERE idx > ? ORDER BY idx DESC LIMIT 1`,
			maxErrorPayload, l.start).Scan(&typ, &payload)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		s := &Step{IsError: typ == errorStepType}
		if s.IsError {
			s.Error = string(payload)
		}
		var one int
		err = db.QueryRow("SELECT 1 FROM steps WHERE idx > ? AND step_type = ? LIMIT 1", l.start, plannerResponseStepType).Scan(&one)
		s.ReachedModel = err == nil
		return s, nil
	})
	if err != nil {
		return nil, "Could not read the Antigravity session log after the prompt."
	}
	return step, ""
}

// readSteps opens the database read-only, runs read, and closes it. ok is false for a database without a steps table.
func readSteps[T any](path string, read func(*sql.DB) (T, error)) (result T, ok bool, err error) {
	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(100)", "query_only(1)"}}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
	if err != nil {
		return result, false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var one int
	switch err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'steps'").Scan(&one); {
	case err == sql.ErrNoRows:
		return result, false, nil
	case err != nil:
		return result, false, err
	}
	result, err = read(db)
	return result, err == nil, err
}
