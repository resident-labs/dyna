// Package doctor diagnoses load and performance problems. This file keeps
// the per-minute samples that conditions and trends read.
package doctor

import (
	"context"
	"database/sql"
	"os"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sysload"
)

// Keep is how long samples stay.
const Keep = 24 * time.Hour

const samplesSchema = `CREATE TABLE IF NOT EXISTS doctor_samples (
 machine TEXT NOT NULL, at INTEGER NOT NULL, grp TEXT NOT NULL,
 cores REAL NOT NULL, memory INTEGER NOT NULL, io REAL NOT NULL,
 psi_cpu REAL NOT NULL, psi_memory REAL NOT NULL, psi_io REAL NOT NULL,
 load REAL, memory_total INTEGER,
 PRIMARY KEY(machine, grp, at)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS doctor_samples_at ON doctor_samples(at);`

// Sample is one group's minute. Load (the 1-minute load average per core)
// and MemoryTotal are set only on the machine group.
type Sample struct {
	At          time.Time
	Group       sysload.Group
	Load        float64
	MemoryTotal uint64
}

// Samples stores samples in the shared dyna.db of DataDir.
type Samples struct{ DataDir string }

// Migrate brings the doctor sample tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Samples{DataDir: dataDir}.open()
	return err
}

func (s Samples) open() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "doctor_samples.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), samplesSchema)
		return err
	})
}

// Record stores the groups of l for machine and drops samples older than Keep.
func (s Samples) Record(machine string, l sysload.Load) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	at := l.At.Unix()
	return database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		ctx := context.Background()
		for _, g := range l.Groups {
			var load, total any
			if g.Name == "machine" && l.CPUs > 0 {
				load, total = l.Load[0]/float64(l.CPUs), l.Memory.Total
			}
			if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO doctor_samples VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				machine, at, g.Name, g.Cores, g.Memory, g.IO, g.Pressure.CPU, g.Pressure.Memory, g.Pressure.IO, load, total); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM doctor_samples WHERE at < ?`, l.At.Add(-Keep).Unix())
		return err
	})
}

// Recorder returns a sysload Record function that stores samples under this
// host's name. It logs a failure once until it changes or clears.
func (s Samples) Recorder(logf func(format string, args ...any)) func(sysload.Load) {
	machine, _ := os.Hostname()
	last := ""
	return func(l sysload.Load) {
		msg := ""
		if err := s.Record(machine, l); err != nil {
			msg = err.Error()
		}
		if msg != "" && msg != last {
			logf("doctor samples: %s", msg)
		}
		last = msg
	}
}

// Window returns one group's samples of machine from since on, oldest first.
func (s Samples) Window(machine, group string, since time.Time) ([]Sample, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT at, cores, memory, io, psi_cpu, psi_memory, psi_io, COALESCE(load,0), COALESCE(memory_total,0)
 FROM doctor_samples WHERE machine=? AND grp=? AND at>=? ORDER BY at`, machine, group, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var at int64
		x := Sample{Group: sysload.Group{Name: group}}
		g := &x.Group
		if err := rows.Scan(&at, &g.Cores, &g.Memory, &g.IO, &g.Pressure.CPU, &g.Pressure.Memory, &g.Pressure.IO, &x.Load, &x.MemoryTotal); err != nil {
			return nil, err
		}
		x.At = time.Unix(at, 0).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}
