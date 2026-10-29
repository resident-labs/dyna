package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Pruned counts what one kind of removal freed in a pass.
type Pruned struct {
	Items int
	Bytes int64
}

func (r *Result) add(kind string, p Pruned) {
	if r.Pruned == nil {
		r.Pruned = map[string]Pruned{}
	}
	total := r.Pruned[kind]
	total.Items += p.Items
	total.Bytes += p.Bytes
	r.Pruned[kind] = total
	r.BytesFreed += p.Bytes
}

// Summary is one line: what each kind freed, then why items were kept.
func (r Result) Summary() string {
	kinds := make([]string, 0, len(r.Pruned))
	for kind := range r.Pruned {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := []string{}
	for _, kind := range kinds {
		if p := r.Pruned[kind]; p.Items > 0 {
			parts = append(parts, fmt.Sprintf("%s %d (%s)", kind, p.Items, size(p.Bytes)))
		}
	}
	text := "freed " + size(r.BytesFreed)
	if len(parts) > 0 {
		text += ": " + strings.Join(parts, ", ")
	}
	kept := 0
	for _, n := range r.Kept {
		kept += n
	}
	if kept > 0 {
		text += fmt.Sprintf("; kept %d", kept)
	}
	return text
}

func size(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

// Report is one saved maintenance pass.
type Report struct {
	At         time.Time
	Duration   time.Duration
	BytesFreed int64
	Summary    string
	Error      string
	Steps      []Step
}

// keepReports bounds the report table; one pass runs about every hour.
const keepReports = 500

const reportSchema = `CREATE TABLE maintenance_reports (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 at_ms INTEGER NOT NULL, duration_ms INTEGER NOT NULL, bytes_freed INTEGER NOT NULL,
 summary TEXT NOT NULL, error TEXT NOT NULL
);`

// stepSchema is version 2. Each row is one timed step of a saved pass.
const stepSchema = `CREATE TABLE maintenance_steps (
 report_id INTEGER NOT NULL, seq INTEGER NOT NULL,
 name TEXT NOT NULL, duration_ms INTEGER NOT NULL, error TEXT NOT NULL,
 PRIMARY KEY (report_id, seq)
);`

// Migrate brings the maintenance report tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := reportDB(dataDir)
	return err
}

func reportDB(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "maintenance.reports.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old == 0 {
			if _, err := conn.ExecContext(ctx, reportSchema); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, stepSchema)
		return err
	})
	return db, err
}

// SaveReport records one pass and drops reports beyond keepReports.
func SaveReport(dataDir string, at time.Time, took time.Duration, r Result, runErr error) error {
	db, err := reportDB(dataDir)
	if err != nil {
		return err
	}
	text := ""
	if runErr != nil {
		text = runErr.Error()
	}
	// Maintenance has no change-bus consumer, so this write emits no event.
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		res, err := conn.ExecContext(ctx, `INSERT INTO maintenance_reports(at_ms,duration_ms,bytes_freed,summary,error) VALUES(?,?,?,?,?)`,
			at.UnixMilli(), took.Milliseconds(), r.BytesFreed, r.Summary(), text)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for seq, step := range r.Steps {
			if _, err := conn.ExecContext(ctx, `INSERT INTO maintenance_steps(report_id,seq,name,duration_ms,error) VALUES(?,?,?,?,?)`,
				id, seq, step.Name, step.Took.Milliseconds(), step.Error); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM maintenance_reports WHERE id <= (SELECT MAX(id) FROM maintenance_reports) - ?`, keepReports); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM maintenance_steps WHERE report_id NOT IN (SELECT id FROM maintenance_reports)`)
		return err
	})
}

// RecentReports returns up to n reports, newest first.
func RecentReports(dataDir string, n int) ([]Report, error) {
	db, err := reportDB(dataDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id,at_ms,duration_ms,bytes_freed,summary,error FROM maintenance_reports ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	ids := []int64{}
	for rows.Next() {
		var r Report
		var id, at, took int64
		if err := rows.Scan(&id, &at, &took, &r.BytesFreed, &r.Summary, &r.Error); err != nil {
			return nil, err
		}
		r.At, r.Duration = time.UnixMilli(at), time.Duration(took)*time.Millisecond
		out = append(out, r)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		if out[i].Steps, err = reportSteps(db, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reportSteps returns the timed steps of one saved pass, in the order they ran.
func reportSteps(db *sql.DB, id int64) ([]Step, error) {
	rows, err := db.Query(`SELECT name,duration_ms,error FROM maintenance_steps WHERE report_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := []Step{}
	for rows.Next() {
		var s Step
		var took int64
		if err := rows.Scan(&s.Name, &took, &s.Error); err != nil {
			return nil, err
		}
		s.Took = time.Duration(took) * time.Millisecond
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

// dryPassKey names a kind's first pass at one retention age.
func dryPassKey(kind string, age time.Duration) string {
	return "maintenance.dry." + kind + "." + age.String()
}

// worktreeTurnKey stores the pass count that picks the first repository for the
// worktree cleanup. It lives in the report database so every hook process reads
// the same turn.
const worktreeTurnKey = "maintenance.worktree.turn"

// nextWorktreeTurn returns this pass's worktree turn and stores the next one.
func nextWorktreeTurn(ctx context.Context, dataDir string) (int, error) {
	db, err := reportDB(dataDir)
	if err != nil {
		return 0, err
	}
	turn := 0
	// Maintenance has no change-bus consumer, so this write emits no event.
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var raw string
		err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, worktreeTurnKey).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if turn, err = strconv.Atoi(raw); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, worktreeTurnKey, strconv.Itoa(turn+1))
		return err
	})
	return turn, err
}

// sweepWithDryPass runs a pruning kind. Its first pass at a new age only
// measures, and the report names it "<kind> (dry run)", so the real numbers
// are seen once before anything is removed. The caller times the step.
func sweepWithDryPass(ctx context.Context, result *Result, kind string, age time.Duration, sweep func(ctx context.Context, dry bool) (Pruned, error)) error {
	db, err := reportDB(store.DataDir())
	if err != nil {
		return err
	}
	key := dryPassKey(kind, age)
	var marked string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&marked)
	dry := err == sql.ErrNoRows
	if err != nil && !dry {
		return err
	}
	freed, err := sweep(ctx, dry)
	if !dry {
		result.add(kind, freed)
		return err
	}
	if result.Pruned == nil {
		result.Pruned = map[string]Pruned{}
	}
	result.Pruned[kind+" (dry run)"] = freed // measured only; not added to BytesFreed
	if err != nil {
		return err
	}
	// Maintenance has no change-bus consumer, so this write emits no event.
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key,value) VALUES(?, '1')`, key)
		return err
	})
}
