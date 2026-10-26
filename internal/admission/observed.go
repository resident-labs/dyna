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
 at INTEGER NOT NULL, running INTEGER NOT NULL, lim INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS admission_observed_at ON admission_observed(at);`

// Observation is one sample in which the turns running were at or over the
// limit, so a new turn would have been held for the sample's interval.
type Observation struct {
	At      time.Time
	Running int
	Limit   int
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
func (o Observed) Record(ctx context.Context, ob Observation) error {
	db, err := o.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: "admission"}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO admission_observed VALUES (?,?,?)`, ob.At.Unix(), ob.Running, ob.Limit); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM admission_observed WHERE at < ?`, ob.At.Add(-KeepObserved).Unix())
		return err
	})
}

// Summary is what the Settings row shows.
type Summary struct {
	// Samples is how many samples since the cutoff were at or over the limit;
	// each stands for SampleEvery of time in which new turns would have waited.
	Samples int
	// MostOver is the most turns that ran past the limit in one of them.
	MostOver int
}

// Held is the time the samples stand for.
func (s Summary) Held() time.Duration { return time.Duration(s.Samples) * SampleEvery }

// Summarize counts the observations since the cutoff.
func (o Observed) Summarize(since time.Time) (Summary, error) {
	db, err := o.open()
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(MAX(running-lim,0)),0) FROM admission_observed WHERE at >= ?`, since.Unix()).Scan(&s.Samples, &s.MostOver)
	return s, err
}
