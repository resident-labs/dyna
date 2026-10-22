// Package attempts stores immutable account admission facts in dyna.db.
// Facts belong to canonical session turns or runs and own no launch resources.
package attempts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
)

const schema = `CREATE TABLE account_attempts (
 admission_id TEXT PRIMARY KEY NOT NULL CHECK(admission_id <> ''),
 session TEXT,
 turn INTEGER,
 run TEXT,
 agent INTEGER,
 account_id TEXT NOT NULL,
 identity TEXT NOT NULL,
 profile TEXT NOT NULL,
 harness TEXT NOT NULL,
 repo TEXT NOT NULL,
 work_dir TEXT NOT NULL,
 admitted_at TEXT NOT NULL,
 CHECK (
  (session IS NOT NULL AND session <> '' AND turn IS NOT NULL AND turn > 0
   AND run IS NULL AND agent IS NULL)
  OR
  (run IS NOT NULL AND run <> '' AND agent IS NOT NULL AND agent > 0
   AND session IS NULL AND turn IS NULL)
 ),
 FOREIGN KEY (session, turn) REFERENCES session_turns(session, n) ON DELETE CASCADE,
 FOREIGN KEY (run) REFERENCES runs(id) ON DELETE CASCADE
);
CREATE INDEX account_attempts_turn ON account_attempts(session, turn) WHERE session IS NOT NULL;
CREATE INDEX account_attempts_run ON account_attempts(run) WHERE run IS NOT NULL;`

// ErrConflict means an admission ID already names different immutable facts.
var ErrConflict = errors.New("admission ID has different immutable facts")

// Fact records the engine's account selection for one native process attempt.
type Fact struct {
	callspec.ExecutionRef
	AccountID  string    `json:"accountId"`
	Identity   string    `json:"identity"`
	Profile    string    `json:"profile"`
	Harness    string    `json:"harness"`
	Repo       string    `json:"repo"`
	WorkDir    string    `json:"workDir"`
	AdmittedAt time.Time `json:"admittedAt"`
}

// Migrate creates the admission relation after its canonical parents exist.
// Only the engine calls it, at startup.
func Migrate(dataDir string) error {
	if dataDir == "" {
		return errors.New("account attempts need a data directory")
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "account_attempts.version", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		for _, parent := range []string{"session_turns", "runs"} {
			exists, err := database.HasTable(ctx, conn, parent)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("account attempts need canonical parent table %s", parent)
			}
		}
		_, err := conn.ExecContext(ctx, schema)
		return err
	})
}

// SaveTx records a fact on the engine's state.Write connection. The engine
// validates caller authority and account policy before calling it. Replays
// return the original fact, including AdmittedAt; changed client facts fail.
// New timestamps are stored with nanosecond precision in UTC.
func SaveTx(ctx context.Context, conn *sql.Conn, fact Fact) (Fact, error) {
	if err := validate(fact.ExecutionRef); err != nil {
		return Fact{}, err
	}
	stored, err := lookup(ctx, conn, fact.AdmissionID)
	if err == nil {
		fact.AdmittedAt = stored.AdmittedAt
		if fact != stored {
			return Fact{}, ErrConflict
		}
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Fact{}, err
	}
	fact.AdmittedAt = fact.AdmittedAt.Round(0).UTC()
	_, err = conn.ExecContext(ctx, `INSERT INTO account_attempts
(admission_id,session,turn,run,agent,account_id,identity,profile,harness,repo,work_dir,admitted_at)
VALUES (?,NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),NULLIF(?,0),?,?,?,?,?,?,?)`,
		fact.AdmissionID, fact.Session, fact.Turn, fact.Run, fact.Agent,
		fact.AccountID, fact.Identity, fact.Profile, fact.Harness, fact.Repo,
		fact.WorkDir, fact.AdmittedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Fact{}, err
	}
	return fact, nil
}

// Load reads an admitted fact for its complete execution reference. AdmissionID
// is required. A missing fact returns sql.ErrNoRows; a changed execution returns
// ErrConflict. Load never migrates the admission relation.
func Load(ctx context.Context, dataDir string, ref callspec.ExecutionRef) (Fact, error) {
	if err := validate(ref); err != nil {
		return Fact{}, err
	}
	if dataDir == "" {
		return Fact{}, errors.New("account attempts need a data directory")
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return Fact{}, err
	}
	var fact Fact
	err = state.For(db).Read(ctx, func(reader state.Reader) error {
		var err error
		fact, err = lookup(ctx, reader, ref.AdmissionID)
		return err
	})
	if err != nil {
		return Fact{}, err
	}
	if fact.ExecutionRef != ref {
		return Fact{}, ErrConflict
	}
	return fact, nil
}

func validate(ref callspec.ExecutionRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.AdmissionID == "" {
		return errors.New("account attempt needs an admission ID")
	}
	return nil
}

func lookup(ctx context.Context, reader state.Reader, id string) (Fact, error) {
	var fact Fact
	var admitted string
	err := reader.QueryRowContext(ctx, `SELECT admission_id,COALESCE(session,''),COALESCE(turn,0),
COALESCE(run,''),COALESCE(agent,0),account_id,identity,profile,harness,repo,work_dir,admitted_at
FROM account_attempts WHERE admission_id=?`, id).Scan(
		&fact.AdmissionID, &fact.Session, &fact.Turn, &fact.Run, &fact.Agent,
		&fact.AccountID, &fact.Identity, &fact.Profile, &fact.Harness, &fact.Repo,
		&fact.WorkDir, &admitted)
	if err != nil {
		return Fact{}, err
	}
	fact.AdmittedAt, err = time.Parse(time.RFC3339Nano, admitted)
	if err != nil {
		return Fact{}, fmt.Errorf("account attempt %s admitted time: %w", id, err)
	}
	return fact, nil
}
