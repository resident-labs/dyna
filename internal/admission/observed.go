package admission

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// KeepObserved is how long observations stay.
const KeepObserved = 7 * 24 * time.Hour

const observedSchema = `CREATE TABLE IF NOT EXISTS admission_observed (
 at INTEGER NOT NULL, pool TEXT NOT NULL, room TEXT NOT NULL,
 held INTEGER NOT NULL, lim INTEGER NOT NULL, forced INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS admission_observed_at ON admission_observed(at);`

// Observation is one turn that the gate would have held (observe mode), or
// held until its wait ended (enforce mode).
type Observation struct {
	At     time.Time
	Pool   Pool
	Room   string
	Held   int
	Limit  int
	Forced bool
}

// Observed stores observations in the shared dyna.db of DataDir.
type Observed struct{ DataDir string }

func (o Observed) open() (*sql.DB, error) {
	db, err := database.Open(o.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "admission_observed.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), observedSchema)
		return err
	})
}

// MigrateObserved brings the table to this version. Only the engine calls it,
// at start.
func MigrateObserved(dataDir string) error {
	_, err := Observed{DataDir: dataDir}.open()
	return err
}

// Record stores one observation and drops those older than KeepObserved.
func (o Observed) Record(ob Observation) error {
	db, err := o.open()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return state.For(db).Write(ctx, hub.Event{Topic: "admission"}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO admission_observed VALUES (?,?,?,?,?,?)`,
			ob.At.Unix(), string(ob.Pool), ob.Room, ob.Held, ob.Limit, ob.Forced); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM admission_observed WHERE at < ?`, ob.At.Add(-KeepObserved).Unix())
		return err
	})
}

// Summary is what the Settings row shows.
type Summary struct {
	// Turns is how many turns the gate held or would have held since the cutoff.
	Turns int
	// MostOver is the largest number of turns that ran above the limit when
	// one of them was held.
	MostOver int
}

// Summarize counts the observations since the cutoff.
func (o Observed) Summarize(since time.Time) (Summary, error) {
	db, err := o.open()
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(held-lim+1),0) FROM admission_observed WHERE at >= ?`, since.Unix()).Scan(&s.Turns, &s.MostOver)
	return s, err
}
