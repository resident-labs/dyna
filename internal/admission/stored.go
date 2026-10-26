package admission

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const stateSchema = `CREATE TABLE IF NOT EXISTS admission_state (
 pool TEXT PRIMARY KEY, overloaded INTEGER NOT NULL, calm INTEGER NOT NULL,
 turn_bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL
)`

// StateStore keeps the gate's State between decisions in the shared dyna.db.
// Every call takes a context, and a caller bounds it: a busy database never
// delays a turn longer than the bound.
type StateStore struct{ DataDir string }

func (s StateStore) open() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "admission_state.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), stateSchema)
		return err
	})
}

// MigrateState brings the table to this version. Only the engine calls it, at
// start.
func MigrateState(dataDir string) error {
	_, err := StateStore{DataDir: dataDir}.open()
	return err
}

// Load returns the saved state of pool, or the zero State when there is none.
func (s StateStore) Load(ctx context.Context, pool Pool) (State, error) {
	db, err := s.open()
	if err != nil {
		return State{}, err
	}
	var st State
	var measured int64
	err = db.QueryRowContext(ctx, `SELECT overloaded, calm, turn_bytes, measured_at FROM admission_state WHERE pool=?`, string(pool)).
		Scan(&st.Overloaded, &st.Calm, &st.TurnBytes, &measured)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil
	}
	if measured > 0 {
		st.MeasuredAt = time.Unix(measured, 0)
	}
	return st, err
}

// Save stores the state of pool.
func (s StateStore) Save(ctx context.Context, pool Pool, st State) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	var measured int64
	if !st.MeasuredAt.IsZero() {
		measured = st.MeasuredAt.Unix()
	}
	return state.For(db).Write(ctx, hub.Event{Topic: "admission"}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO admission_state VALUES (?,?,?,?,?)`,
			string(pool), st.Overloaded, st.Calm, st.TurnBytes, measured)
		return err
	})
}
