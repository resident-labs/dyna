package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// KeepReports is how long reports stay.
const KeepReports = 30 * 24 * time.Hour

const reportsSchema = `CREATE TABLE IF NOT EXISTS doctor_reports (
 id INTEGER PRIMARY KEY, at INTEGER NOT NULL, machine TEXT NOT NULL,
 target TEXT NOT NULL, fired_by TEXT NOT NULL, if_condition TEXT NOT NULL,
 fingerprint TEXT NOT NULL, healthy INTEGER NOT NULL, summary TEXT NOT NULL,
 findings TEXT NOT NULL CHECK(json_valid(findings))
);
CREATE INDEX IF NOT EXISTS doctor_reports_cooldown ON doctor_reports(target, fingerprint, at);
CREATE INDEX IF NOT EXISTS doctor_reports_at ON doctor_reports(at);`

// Reports stores reports in the shared dyna.db of DataDir.
type Reports struct{ DataDir string }

func (s Reports) open() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "doctor_reports.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), reportsSchema)
		return err
	})
}

// Save stores r, drops reports older than KeepReports, and returns r's ID.
func (s Reports) Save(r Report) (int64, error) {
	db, err := s.open()
	if err != nil {
		return 0, err
	}
	findings, err := json.Marshal(r.Findings)
	if err != nil {
		return 0, err
	}
	if r.Findings == nil {
		findings = []byte("[]")
	}
	var id int64
	ctx := context.Background()
	err = state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `INSERT INTO doctor_reports(at, machine, target, fired_by, if_condition, fingerprint, healthy, summary, findings) VALUES (?,?,?,?,?,?,?,?,?)`,
			r.At.Unix(), r.Machine, r.Target, r.Trigger, r.Condition, r.Fingerprint, r.Healthy, r.Summary, string(findings))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM doctor_reports WHERE at < ?`, r.At.Add(-KeepReports).Unix())
		return err
	})
	return id, err
}

// Recent returns the newest report of target with fingerprint from since on.
func (s Reports) Recent(machine, target, fingerprint string, since time.Time) (Report, bool, error) {
	list, err := s.list(`WHERE machine=? AND target=? AND fingerprint=? AND at>=? ORDER BY at DESC, id DESC LIMIT 1`, machine, target, fingerprint, since.Unix())
	if err != nil || len(list) == 0 {
		return Report{}, false, err
	}
	return list[0], true, nil
}

// List returns the newest limit reports, newest first.
func (s Reports) List(limit int) ([]Report, error) {
	return s.list(`ORDER BY at DESC, id DESC LIMIT ?`, limit)
}

// Get returns one report.
func (s Reports) Get(id int64) (Report, bool, error) {
	list, err := s.list(`WHERE id=?`, id)
	if err != nil || len(list) == 0 {
		return Report{}, false, err
	}
	return list[0], true, nil
}

func (s Reports) list(where string, args ...any) ([]Report, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, at, machine, target, fired_by, if_condition, fingerprint, healthy, summary, findings FROM doctor_reports `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		var at int64
		var findings string
		if err := rows.Scan(&r.ID, &at, &r.Machine, &r.Target, &r.Trigger, &r.Condition, &r.Fingerprint, &r.Healthy, &r.Summary, &findings); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0).UTC()
		if err := json.Unmarshal([]byte(findings), &r.Findings); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
