// Package cihealth keeps the facts a CI health view needs in dyna.db: one
// row per CI run, one per job with its steps, and one per near-limit test.
// The watcher fills them from the reports and job lists it already reads, so
// a slow test is shown as a trend instead of filed as a room issue.
//
// Only tests over half the run's limit are kept. A full report holds
// thousands of fast tests, and they would drown the rows that matter.
package cihealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const schemaVersion = 1

// KeepTests bounds the history of one test. KeepRuns bounds a room's runs,
// which the CI graph draws. Both are enforced by Prune.
const (
	KeepTests = 30
	KeepRuns  = 200
	KeepDays  = 90
)

const schema = `
CREATE TABLE ci_health_runs (
	room       TEXT    NOT NULL,
	token      TEXT    NOT NULL,
	repo       TEXT    NOT NULL DEFAULT '',
	pr         INTEGER NOT NULL DEFAULT 0,
	head       TEXT    NOT NULL DEFAULT '',
	workflow   TEXT    NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL,
	status     TEXT    NOT NULL DEFAULT '',
	conclusion TEXT    NOT NULL DEFAULT '',
	queue_ms   INTEGER NOT NULL DEFAULT 0,
	busy_ms    INTEGER NOT NULL DEFAULT 0,
	warn_ms    INTEGER NOT NULL DEFAULT 0,
	fail_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room, token)
);
CREATE INDEX ci_health_runs_time ON ci_health_runs(room, started_at DESC);
CREATE TABLE ci_health_jobs (
	room         TEXT    NOT NULL,
	token        TEXT    NOT NULL,
	job_id       INTEGER NOT NULL,
	name         TEXT    NOT NULL,
	status       TEXT    NOT NULL DEFAULT '',
	conclusion   TEXT    NOT NULL DEFAULT '',
	started_at   INTEGER NOT NULL DEFAULT 0,
	completed_at INTEGER NOT NULL DEFAULT 0,
	busy_ms      INTEGER NOT NULL DEFAULT 0,
	steps        TEXT    NOT NULL DEFAULT '[]',
	PRIMARY KEY (room, token, job_id)
);
CREATE TABLE ci_health_tests (
	room        TEXT    NOT NULL,
	test        TEXT    NOT NULL,
	token       TEXT    NOT NULL,
	package     TEXT    NOT NULL DEFAULT '',
	pr          INTEGER NOT NULL DEFAULT 0,
	head        TEXT    NOT NULL DEFAULT '',
	started_at  INTEGER NOT NULL,
	duration_ms INTEGER NOT NULL,
	limit_ms    INTEGER NOT NULL,
	PRIMARY KEY (room, test, token)
);
CREATE INDEX ci_health_tests_time ON ci_health_tests(room, test, started_at DESC);
CREATE TABLE ci_health_notified (
	room  TEXT    NOT NULL,
	test  TEXT    NOT NULL,
	token TEXT    NOT NULL,
	at    INTEGER NOT NULL,
	PRIMARY KEY (room, test)
);`

// Run is one CI run: what the graph draws and the drill-down opens.
type Run struct {
	Room       string
	Token      string
	Repo       string
	PR         int
	Head       string
	Workflow   string
	Started    time.Time
	Status     string
	Conclusion string
	Queue      time.Duration
	Busy       time.Duration
	Warn       time.Duration
	Fail       time.Duration
}

// Job is one job of a run. Busy sums the steps that ran, so a job still
// waiting for a runner contributes no time. Steps stay in their stated order.
type Job struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	Started    time.Time
	Completed  time.Time
	Busy       time.Duration
	Steps      []Step
}

// Step is one executed step of a job.
type Step struct {
	Name     string
	Duration time.Duration
}

// Test is one near-limit test row, from one run.
type Test struct {
	Room     string
	Test     string
	Package  string
	Token    string
	PR       int
	Head     string
	Started  time.Time
	Duration time.Duration // the isolated re-run time when the job measured one
	Limit    time.Duration
}

// Over reports whether the test broke the room's limit.
func (t Test) Over() bool { return t.Duration > t.Limit }

// Crossing is a test that broke the limit in this run and in another run at
// least gap away: a slow test, not one loaded-host episode.
type Crossing struct {
	Test  string
	Now   Test
	Other Test
}

// RunDetail is one run with its jobs and its slowest tests.
type RunDetail struct {
	Run   Run
	Jobs  []Job
	Tests []Test
}

// TestHistory is one test with its stored runs, newest first.
type TestHistory struct {
	Test    string
	Package string
	Runs    []Test
}

// Store keeps CI health rows in dyna.db.
type Store struct{ DataDir string }

// Migrate brings the CI health tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "cihealth.version", schemaVersion, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

type stepJSON struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

func marshalSteps(steps []Step) (string, error) {
	rows := make([]stepJSON, 0, len(steps))
	for _, step := range steps {
		rows = append(rows, stepJSON{Name: step.Name, Ms: step.Duration.Milliseconds()})
	}
	raw, err := json.Marshal(rows)
	return string(raw), err
}

func unmarshalSteps(raw string) ([]Step, error) {
	var rows []stepJSON
	if raw == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	steps := make([]Step, 0, len(rows))
	for _, row := range rows {
		steps = append(steps, Step{Name: row.Name, Duration: time.Duration(row.Ms) * time.Millisecond})
	}
	return steps, nil
}

// SaveRun writes one run and replaces its jobs, so a re-inspected run has no
// stale job rows. Test rows are written by SaveTests in the same tick.
func (s Store) SaveRun(ctx context.Context, run Run, jobs []Job) error {
	if run.Room == "" || run.Token == "" {
		return errors.New("a CI health run needs a room and a token")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO ci_health_runs
		(room, token, repo, pr, head, workflow, started_at, status, conclusion, queue_ms, busy_ms, warn_ms, fail_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(room, token) DO UPDATE SET
			repo=excluded.repo, pr=excluded.pr, head=excluded.head, workflow=excluded.workflow,
			started_at=excluded.started_at, status=excluded.status, conclusion=excluded.conclusion,
			queue_ms=excluded.queue_ms, busy_ms=excluded.busy_ms, warn_ms=excluded.warn_ms, fail_ms=excluded.fail_ms`,
			run.Room, run.Token, run.Repo, run.PR, run.Head, run.Workflow, unix(run.Started), run.Status, run.Conclusion,
			run.Queue.Milliseconds(), run.Busy.Milliseconds(), run.Warn.Milliseconds(), run.Fail.Milliseconds())
		if err != nil {
			return fmt.Errorf("save CI run %s: %w", run.Token, err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM ci_health_jobs WHERE room=? AND token=?`, run.Room, run.Token); err != nil {
			return err
		}
		for _, job := range jobs {
			steps, err := marshalSteps(job.Steps)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO ci_health_jobs
			(room, token, job_id, name, status, conclusion, started_at, completed_at, busy_ms, steps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				run.Room, run.Token, job.ID, job.Name, job.Status, job.Conclusion,
				unix(job.Started), unix(job.Completed), job.Busy.Milliseconds(), steps); err != nil {
				return fmt.Errorf("save CI job %s: %w", job.Name, err)
			}
		}
		return nil
	})
}

// SaveTests records the near-limit tests of one run. A repeated test keeps
// its longest time, so a retried test cannot shrink below its worst run.
func (s Store) SaveTests(ctx context.Context, tests []Test) error {
	if len(tests) == 0 {
		return nil
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for _, test := range tests {
			if test.Room == "" || test.Test == "" || test.Token == "" {
				return errors.New("a CI health test needs a room, a test and a token")
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO ci_health_tests
			(room, test, token, package, pr, head, started_at, duration_ms, limit_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room, test, token) DO UPDATE SET
				package=excluded.package, pr=excluded.pr, head=excluded.head, started_at=excluded.started_at,
				duration_ms=MAX(ci_health_tests.duration_ms, excluded.duration_ms), limit_ms=excluded.limit_ms`,
				test.Room, test.Test, test.Token, test.Package, test.PR, test.Head, unix(test.Started),
				test.Duration.Milliseconds(), test.Limit.Milliseconds()); err != nil {
				return fmt.Errorf("save CI test %s: %w", test.Test, err)
			}
		}
		return nil
	})
}

// Runs returns a room's runs, newest first, up to limit rows.
func (s Store) Runs(ctx context.Context, room string, limit int) ([]Run, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > KeepRuns {
		limit = KeepRuns
	}
	rows, err := db.QueryContext(ctx, `SELECT token, repo, pr, head, workflow, started_at, status, conclusion, queue_ms, busy_ms, warn_ms, fail_ms
		FROM ci_health_runs WHERE room=? ORDER BY started_at DESC LIMIT ?`, room, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows, room)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Jobs returns the jobs of several runs at once, keyed by run token. The CI
// graph draws one band per job, so a room's graph is one query, not one per run.
func (s Store) Jobs(ctx context.Context, room string, tokens []string) (map[string][]Job, error) {
	out := map[string][]Job{}
	if len(tokens) == 0 {
		return out, nil
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	query := `SELECT token, job_id, name, status, conclusion, started_at, completed_at, busy_ms, steps
		FROM ci_health_jobs WHERE room=? AND token IN (?` + placeholders(len(tokens)-1) + `)
		ORDER BY started_at, job_id`
	args := make([]any, 0, len(tokens)+1)
	args = append(args, room)
	for _, token := range tokens {
		args = append(args, token)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var token string
		var job Job
		var started, completed, busy int64
		var steps string
		if err := rows.Scan(&token, &job.ID, &job.Name, &job.Status, &job.Conclusion, &started, &completed, &busy, &steps); err != nil {
			return nil, err
		}
		job.Started, job.Completed = moment(started), moment(completed)
		job.Busy = time.Duration(busy) * time.Millisecond
		if job.Steps, err = unmarshalSteps(steps); err != nil {
			return nil, fmt.Errorf("job %s steps: %w", job.Name, err)
		}
		out[token] = append(out[token], job)
	}
	return out, rows.Err()
}

// Run returns one run with its jobs and its slowest tests.
func (s Store) Run(ctx context.Context, room, token string) (RunDetail, error) {
	var detail RunDetail
	db, err := s.db()
	if err != nil {
		return detail, err
	}
	row := db.QueryRowContext(ctx, `SELECT token, repo, pr, head, workflow, started_at, status, conclusion, queue_ms, busy_ms, warn_ms, fail_ms
		FROM ci_health_runs WHERE room=? AND token=?`, room, token)
	detail.Run, err = scanRun(row, room)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, fmt.Errorf("CI run %s is not stored", token)
	}
	if err != nil {
		return detail, err
	}
	detail.Jobs, err = s.jobs(ctx, db, room, token)
	if err != nil {
		return detail, err
	}
	detail.Tests, err = s.tests(ctx, db, room, token)
	return detail, err
}

func (s Store) jobs(ctx context.Context, db *sql.DB, room, token string) ([]Job, error) {
	rows, err := db.QueryContext(ctx, `SELECT job_id, name, status, conclusion, started_at, completed_at, busy_ms, steps
		FROM ci_health_jobs WHERE room=? AND token=? ORDER BY started_at, job_id`, room, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var job Job
		var started, completed, busy int64
		var steps string
		if err := rows.Scan(&job.ID, &job.Name, &job.Status, &job.Conclusion, &started, &completed, &busy, &steps); err != nil {
			return nil, err
		}
		job.Started, job.Completed = moment(started), moment(completed)
		job.Busy = time.Duration(busy) * time.Millisecond
		if job.Steps, err = unmarshalSteps(steps); err != nil {
			return nil, fmt.Errorf("job %s steps: %w", job.Name, err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s Store) tests(ctx context.Context, db *sql.DB, room, token string) ([]Test, error) {
	rows, err := db.QueryContext(ctx, `SELECT test, package, token, pr, head, started_at, duration_ms, limit_ms
		FROM ci_health_tests WHERE room=? AND token=? ORDER BY duration_ms DESC, test`, room, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Test
	for rows.Next() {
		var test Test
		test.Room = room
		var started, duration, limit int64
		if err := rows.Scan(&test.Test, &test.Package, &test.Token, &test.PR, &test.Head, &started, &duration, &limit); err != nil {
			return nil, err
		}
		test.Started = moment(started)
		test.Duration = time.Duration(duration) * time.Millisecond
		test.Limit = time.Duration(limit) * time.Millisecond
		out = append(out, test)
	}
	return out, rows.Err()
}

// Tests returns one history per test, newest test run first. Each history
// keeps its newest KeepTests rows.
func (s Store) Tests(ctx context.Context, room string) ([]TestHistory, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().AddDate(0, 0, -KeepDays).Unix()
	rows, err := db.QueryContext(ctx, `SELECT test, package, token, pr, head, started_at, duration_ms, limit_ms
		FROM ci_health_tests WHERE room=? AND started_at>=? ORDER BY test, started_at DESC`, room, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestHistory
	for rows.Next() {
		var test Test
		test.Room = room
		var started, duration, limit int64
		if err := rows.Scan(&test.Test, &test.Package, &test.Token, &test.PR, &test.Head, &started, &duration, &limit); err != nil {
			return nil, err
		}
		test.Started = moment(started)
		test.Duration = time.Duration(duration) * time.Millisecond
		test.Limit = time.Duration(limit) * time.Millisecond
		if len(out) == 0 || out[len(out)-1].Test != test.Test {
			out = append(out, TestHistory{Test: test.Test, Package: test.Package})
		}
		last := &out[len(out)-1]
		if len(last.Runs) < KeepTests {
			last.Runs = append(last.Runs, test)
		}
	}
	return out, rows.Err()
}

// Crossings returns tests that broke the limit in this run and in another run
// at least gap away, among the named tests. A test already reported stays out
// until Prune clears it after a quiet week.
func (s Store) Crossings(ctx context.Context, room, token string, tests []string, gap time.Duration) ([]Crossing, error) {
	if len(tests) == 0 {
		return nil, nil
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	notified, err := s.Notified(ctx, room, tests)
	if err != nil {
		return nil, err
	}
	var out []Crossing
	for _, name := range tests {
		if notified[name] {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT token, package, pr, head, started_at, duration_ms, limit_ms
			FROM ci_health_tests WHERE room=? AND test=? AND duration_ms>limit_ms ORDER BY started_at DESC LIMIT ?`,
			room, name, KeepTests)
		if err != nil {
			return nil, err
		}
		var breaches []Test
		for rows.Next() {
			var test Test
			test.Room, test.Test = room, name
			var started, duration, limit int64
			if err := rows.Scan(&test.Token, &test.Package, &test.PR, &test.Head, &started, &duration, &limit); err != nil {
				rows.Close()
				return nil, err
			}
			test.Started = moment(started)
			test.Duration = time.Duration(duration) * time.Millisecond
			test.Limit = time.Duration(limit) * time.Millisecond
			breaches = append(breaches, test)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var now *Test
		for i := range breaches {
			if breaches[i].Token == token {
				now = &breaches[i]
				break
			}
		}
		if now == nil {
			continue
		}
		// The nearest breach that a loaded-host episode cannot explain.
		for i := range breaches {
			if breaches[i].Token == token {
				continue
			}
			if abs(breaches[i].Started.Sub(now.Started)) >= gap {
				out = append(out, Crossing{Test: name, Now: *now, Other: breaches[i]})
				break
			}
		}
	}
	return out, nil
}

// Notified reports which tests already sent a crossing line.
func (s Store) Notified(ctx context.Context, room string, tests []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(tests) == 0 {
		return out, nil
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	query := `SELECT test FROM ci_health_notified WHERE room=? AND test IN (?` + placeholders(len(tests)-1) + `)`
	args := make([]any, 0, len(tests)+1)
	args = append(args, room)
	for _, test := range tests {
		args = append(args, test)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var test string
		if err := rows.Scan(&test); err != nil {
			return nil, err
		}
		out[test] = true
	}
	return out, rows.Err()
}

// MarkNotified records that one crossing line named each test.
func (s Store) MarkNotified(ctx context.Context, room string, tests []string, token string) error {
	if len(tests) == 0 {
		return nil
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for _, test := range tests {
			if _, err := conn.ExecContext(ctx, `INSERT INTO ci_health_notified (room, test, token, at) VALUES (?, ?, ?, ?)
				ON CONFLICT(room, test) DO UPDATE SET token=excluded.token, at=excluded.at`,
				room, test, token, time.Now().Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prune drops rows past the retention window and clears a test's reported
// marker once it has been quiet for a week, so a later breach reports again.
func (s Store) Prune(ctx context.Context, now time.Time) (int, error) {
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -KeepDays).Unix()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM ci_health_tests WHERE started_at<?`, []any{cutoff}},
		{`DELETE FROM ci_health_tests WHERE rowid IN (
			SELECT rowid FROM (
				SELECT rowid, ROW_NUMBER() OVER (PARTITION BY room, test ORDER BY started_at DESC) AS n
				FROM ci_health_tests
			) WHERE n>? )`, []any{KeepTests}},
		{`DELETE FROM ci_health_runs WHERE started_at<?`, []any{cutoff}},
		{`DELETE FROM ci_health_runs WHERE rowid IN (
			SELECT rowid FROM (
				SELECT rowid, ROW_NUMBER() OVER (PARTITION BY room ORDER BY started_at DESC) AS n
				FROM ci_health_runs
			) WHERE n>? )`, []any{KeepRuns}},
		{`DELETE FROM ci_health_jobs WHERE NOT EXISTS (
			SELECT 1 FROM ci_health_runs r WHERE r.room=ci_health_jobs.room AND r.token=ci_health_jobs.token)`, nil},
		{`DELETE FROM ci_health_notified WHERE NOT EXISTS (
			SELECT 1 FROM ci_health_tests t WHERE t.room=ci_health_notified.room AND t.test=ci_health_notified.test
			AND t.duration_ms>t.limit_ms AND t.started_at>=?)`, []any{now.AddDate(0, 0, -7).Unix()}},
	}
	total := 0
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for _, statement := range statements {
			result, err := conn.ExecContext(ctx, statement.query, statement.args...)
			if err != nil {
				return err
			}
			n, _ := result.RowsAffected()
			total += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// scanner is the shared row shape of *sql.Row and *sql.Rows.
type scanner interface{ Scan(...any) error }

func scanRun(row scanner, room string) (Run, error) {
	var run Run
	var started, queue, busy, warn, fail int64
	err := row.Scan(&run.Token, &run.Repo, &run.PR, &run.Head, &run.Workflow, &started, &run.Status, &run.Conclusion, &queue, &busy, &warn, &fail)
	run.Room = room
	run.Started = moment(started)
	run.Queue = time.Duration(queue) * time.Millisecond
	run.Busy = time.Duration(busy) * time.Millisecond
	run.Warn = time.Duration(warn) * time.Millisecond
	run.Fail = time.Duration(fail) * time.Millisecond
	return run, err
}

func unix(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.Unix()
}

func moment(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(", ?", n)
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
