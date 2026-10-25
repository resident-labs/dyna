package health

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// One row per profile holds its health record in dyna.db (rule r10).
const schema = `CREATE TABLE harness_health (
 profile TEXT PRIMARY KEY,
 record BLOB NOT NULL CHECK(json_valid(record))
);`

// Service is safe for independent callers and processes. No in-memory cache can
// conceal another process's update. The zero value uses the default paths.
type Service struct{ dir string }

// New uses dir for dyna.db and retained records to import. An empty dir uses
// the data folder for dyna.db and the default cache for import sources.
func New(dir string) *Service { return &Service{dir: dir} }

// Migrate creates the health table. Only the engine runs it, at start.
func Migrate(dataDir string) error {
	dir, err := (&Service{}).sourceDir()
	if err != nil {
		return err
	}
	_, err = open(dataDir, dir)
	return err
}

func open(dataDir, sourceDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "harness.health", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		return importHealthRecords(ctx, conn, sourceDir)
	})
	return db, err
}

func (s *Service) db() (*sql.DB, error) {
	if s.dir != "" {
		return open(s.dir, s.dir)
	}
	dir, err := s.sourceDir()
	if err != nil {
		return nil, err
	}
	return open(store.DataDir(), dir)
}

// key names a profile's record: a complete hash avoids traversal, collisions
// from sanitization, and long names.
func key(name string) string {
	hash := sha256.Sum256([]byte(name))
	return hex.EncodeToString(hash[:])
}

// path is the previous release's file for a profile's record.
func (s *Service) sourceDir() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "dyna", "profile-health"), nil
}

func (s *Service) path(name string) (string, error) {
	dir, err := s.sourceDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key(name)+".json"), nil
}

// TODO: legacy/shim remove later — import retained health records only at migration.
func importHealthRecords(ctx context.Context, conn *sql.Conn, dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 69 || !strings.HasSuffix(name, ".json") {
			continue
		}
		hash := strings.TrimSuffix(name, ".json")
		if _, err := hex.DecodeString(hash); err != nil || strings.ToLower(hash) != hash {
			continue
		}
		var exists bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM harness_health WHERE profile = ?)", hash).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		_, err := state.ImportMigration(ctx, conn, "health/"+hash, filepath.Join(dir, name), func(conn *sql.Conn, raw []byte) error {
			var r record
			if json.Unmarshal(raw, &r) != nil {
				return nil
			}
			_, err := conn.ExecContext(ctx, "INSERT INTO harness_health(profile,record) VALUES(?,?)", hash, raw)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func saveRow(ctx context.Context, conn *sql.Conn, name string, raw []byte) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO harness_health(profile, record) VALUES (?, ?)
ON CONFLICT(profile) DO UPDATE SET record = excluded.record`, key(name), raw)
	return err
}

// readRow reads a profile's record; ok is false when it has none.
func readRow(ctx context.Context, q state.Reader, name string) (r record, ok bool, err error) {
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT record FROM harness_health WHERE profile = ?", key(name)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, true, json.Unmarshal(raw, &r)
}

// View reads persisted evidence and compares it with the current configuration
// and CLI version. It executes no version command.
func (s *Service) View(p profile.Profile, version string) View {
	v := baseView(p, version)
	ctx := context.Background()
	r, ok, err := s.load(ctx, p.Name)
	if err != nil {
		v.StorageError = err.Error()
		return v
	}
	if ok {
		return makeView(p, version, r)
	}
	return v
}

func (s *Service) load(ctx context.Context, name string) (record, bool, error) {
	db, err := s.db()
	if err != nil {
		return record{}, false, err
	}
	return readRow(ctx, db, name)
}

// RecordTurn records a completed ordinary turn. A healthy turn needs successful
// execution and recognized output. It does not need text, tools, or usage.
// Storage failures appear only in the returned diagnostic view.
func (s *Service) RecordTurn(p profile.Profile, version string, o Observation) View {
	return recordObservation(p, version, o, false, false, s.save)
}

// RecordStartFailure records that a turn could not start the runtime. It is an
// unsuccessful observation at the turn's end time, so health stops showing an
// older success, and it carries no CLI version because none was observed.
func (s *Service) RecordStartFailure(p profile.Profile, at time.Time) View {
	return recordObservation(p, "", Observation{At: at, StartFailed: true}, false, false, s.save)
}

// RecordProgress makes current stdout evidence available to a running-session
// banner. It cannot bless a CLI version or advance the last completed turn.
func (s *Service) RecordProgress(p profile.Profile, version string, o Observation) View {
	o.Running = true
	return recordObservation(p, version, o, false, false, s.save)
}

// RecordProbe records a probe whose caller checks stricter criteria. passed
// cannot override unsuccessful execution or an absence of recognized output.
func (s *Service) RecordProbe(p profile.Profile, version string, o Observation, passed bool) View {
	return recordObservation(p, version, o, true, passed, s.save)
}

// Bound optional health writes so storage cannot hold up a turn indefinitely.
const saveWait = time.Second

func recordObservation(p profile.Profile, version string, o Observation, probe, passed bool, save func(string, *record, func()) error) View {
	if o.At.IsZero() {
		o.At = time.Now().UTC()
	}
	if o.RecognizedRecords < 0 {
		o.RecognizedRecords = 0
	}
	r := record{Fingerprint: Fingerprint(p)}
	apply := func() {
		lastTurn := r.Observed.LastTurnAt
		if !probe && !o.Running && o.At.After(lastTurn) {
			lastTurn = o.At
		}
		if !o.At.Before(r.Observed.At) {
			r.Version = version
			r.Observed = Observed{Observation: o, RecognizedOutput: o.RecognizedRecords > 0,
				Probe: probe, ProbePassed: passed}
		}
		r.Observed.LastTurnAt = lastTurn
		if !o.Running && o.Successful && o.RecognizedRecords > 0 && (!probe || passed) && !o.At.Before(r.LastHealthyAt) {
			r.LastHealthyVersion, r.LastHealthyAt = version, o.At
		}
	}
	err := save(p.Name, &r, apply)
	if err != nil {
		// Preserve useful evidence for the caller even when persistence fails.
		apply()
	}
	v := makeView(p, version, r)
	if err != nil {
		v.StorageError = err.Error()
	}
	return v
}

// save merges the observation and commits it through the shared SQLite writer.
func (s *Service) save(name string, r *record, apply func()) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	work, stop := context.WithTimeout(context.Background(), saveWait)
	defer stop()
	return s.saveRecord(work, db, name, r, apply)
}

func (s *Service) saveRecord(work context.Context, db *sql.DB, name string, r *record, apply func()) error {
	return state.For(db).Write(work, hub.Event{}, func(conn *sql.Conn) error {
		existing, ok, err := readRow(work, conn, name)
		if err != nil {
			var syntax *json.SyntaxError
			var wrongType *json.UnmarshalTypeError
			if !errors.As(err, &syntax) && !errors.As(err, &wrongType) {
				return err
			}
			ok = false
		}
		if ok && existing.Fingerprint == r.Fingerprint {
			*r = existing
		}
		apply()
		raw, err := json.Marshal(*r)
		if err != nil {
			return err
		}
		return saveRow(work, conn, name, raw)
	})
}
