package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/procident"
	"dyna-agent/internal/state"
)

// AdmissionTopic is the hub topic an admissions-table write publishes under.
const AdmissionTopic = "admission"

// ErrNoReservation reports that no admitted reservation remains for reattachment.
var ErrNoReservation = errors.New("reservation is missing")

// pollEvery is how long a holder waits between reads of its waiting row. The
// engine's pass writes the row; this only notices.
const pollEvery = time.Second

// reservationMargin gives recovery time beyond a check's own deadline.
const reservationMargin = 5 * time.Minute

// maxPortsPerNode bounds the port indexes a node hands to its checks. The
// index offsets two base ports (sccache and act artifacts) whose ranges then
// never meet; no node runs this many checks at once.
const maxPortsPerNode = 1024

// unlimitedCPUs stands in for a node whose probe read no CPU count, so
// placement treats it as open rather than closed.
const unlimitedCPUs = 1 << 20

// admissionSchema is the queue: one row per local CI check that waits or runs
// on a node. Jobs wait in the jobs table, so the pass reads them as
// candidates and holds them through their running rows. A row's holder is the
// engine (ended by the normal end path of its job or check) or one outside
// process (named by pid and start time, reaped when it is gone).
const admissionSchema = `CREATE TABLE admissions (
	id TEXT PRIMARY KEY,
	node TEXT NOT NULL,
	room TEXT NOT NULL DEFAULT '',
	cpus INTEGER NOT NULL DEFAULT 0,
	memory_mib INTEGER NOT NULL DEFAULT 0,
	heavy INTEGER NOT NULL DEFAULT 0,
	ahead INTEGER NOT NULL DEFAULT 0,
	port INTEGER NOT NULL DEFAULT -1,
	state TEXT NOT NULL CHECK(state IN ('waiting','admitted','released')),
	reason TEXT NOT NULL DEFAULT '',
	engine INTEGER NOT NULL DEFAULT 0,
	owner_pid INTEGER NOT NULL DEFAULT 0,
	owner_start INTEGER NOT NULL DEFAULT 0,
	timeout_ms INTEGER NOT NULL DEFAULT 1800000,
	expires_ms INTEGER NOT NULL DEFAULT 0,
	created_ms INTEGER NOT NULL,
	check_name TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	number INTEGER NOT NULL DEFAULT 0,
	trial INTEGER NOT NULL DEFAULT 0,
	branch TEXT NOT NULL DEFAULT '',
	head TEXT NOT NULL DEFAULT ''
);
CREATE INDEX admissions_open ON admissions(node, state);
CREATE UNIQUE INDEX admissions_port ON admissions(node, port) WHERE state = 'admitted';`

// Demand is what one unit of work asks of a node: a check's clamped resources
// or a job's. Heavy marks IO-heavy work, which a node runs only a few of.
type Demand struct {
	// Node is the registered machine the work runs on.
	Node string
	// ID stays stable for the life of the work, such as a check's run and
	// check ID, so a retry reuses its row.
	ID string
	// Room is the room the work belongs to, so rooms take turns.
	Room      string
	Heavy     bool
	CPUs      int
	MemoryMiB int64
	// Timeout is the check deadline, starting when the check is admitted.
	Timeout time.Duration
	// Ahead moves this unit in front of its own room's other waiting work.
	// Local CI sets it for merge-trial and integration-sync checks, so a
	// merge queue never waits behind its room's pull-request checks. Other
	// rooms keep their turns, and each list stays oldest first.
	Ahead bool
	// Check names the check, so a queue view words its row. Empty keeps the
	// ID as the row's name.
	Check string
	// Source says what the check's run tests, so the same view words the
	// work instead of reading the ID. A zero kind stores no source.
	Source CheckSource
}

// Admission is the engine-side queue of every node: the one place that
// decides which waiting work a node takes. The queue is the admissions table
// plus the jobs table; nothing else holds admission state, so an engine
// restart rebuilds it from the rows that survived. The pass is the only
// writer of admission decisions. Passes serialize through a SQLite write
// transaction, including passes made by standalone check runners.
type Admission struct {
	// DataDir is the engine's data folder. Empty uses the default.
	DataDir string
	// Machines resolves a node's probed capacity and its limits. Nil uses the
	// machine registry of the data folder.
	Machines MachineSource
	// RoomName resolves a holder's room ID for a readable wait reason. Nil
	// keeps the ID.
	RoomName func(string) string
	// Engine marks rows this process holds: they end through the normal end
	// path of their job or check, never through a pid check. A holder outside
	// the engine, such as the manual CI command, leaves this off, so the pass
	// can reap its rows once its process is gone.
	Engine bool
	clock  func() time.Time
}

func (a Admission) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// admissionOwner names the process that holds a row, so a reused pid is not
// taken for a dead holder. Tests replace it.
var admissionOwner = func() (pid int, start uint64) {
	pid = os.Getpid()
	start, _ = procident.StartTime(pid)
	return pid, start
}

// ownerAlive reports whether the process that held a row still runs. Tests
// replace it.
var ownerAlive = func(pid int, start uint64) bool {
	if pid <= 0 || !procident.Alive(pid) {
		return false
	}
	got, err := procident.StartTime(pid)
	return err != nil || got == start
}

// Migrate brings the admissions table of dataDir's dyna.db to this version.
func (a Admission) Migrate() error {
	_, err := a.db()
	return err
}

func (a Admission) db() (*sql.DB, error) {
	dir := a.DataDir
	if dir == "" {
		dir = defaultDataDir()
	}
	db, err := database.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(db, "admissions.version", 3, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), admissionSchema)
			return err
		}
		if old == 1 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE admissions ADD COLUMN ahead INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
		for _, alter := range []string{
			"ALTER TABLE admissions ADD COLUMN check_name TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE admissions ADD COLUMN kind TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE admissions ADD COLUMN number INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE admissions ADD COLUMN trial INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE admissions ADD COLUMN branch TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE admissions ADD COLUMN head TEXT NOT NULL DEFAULT ''",
		} {
			if _, err := conn.ExecContext(context.Background(), alter); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return db, nil
}

func (a Admission) machines() MachineSource {
	if a.Machines != nil {
		return a.Machines
	}
	return machines.Store{DataDir: a.DataDir}
}

// admissionRow is one stored queue row.
type admissionRow struct {
	Demand
	Port    int
	State   string
	Reason  string
	Engine  bool
	PID     int
	Start   uint64
	Created time.Time
}

const admissionColumns = "id,node,room,cpus,memory_mib,heavy,ahead,port,state,reason,engine,owner_pid,owner_start,timeout_ms,expires_ms,created_ms,check_name,kind,number,trial,branch,head"

func scanAdmission(row interface{ Scan(...any) error }) (admissionRow, error) {
	var r admissionRow
	var heavy, ahead, engine, trial int
	var created, timeout, expires int64
	var number int
	if err := row.Scan(&r.ID, &r.Node, &r.Room, &r.CPUs, &r.MemoryMiB, &heavy, &ahead, &r.Port, &r.State, &r.Reason, &engine, &r.PID, &r.Start, &timeout, &expires, &created, &r.Check, &r.Source.Kind, &number, &trial, &r.Source.Branch, &r.Source.Head); err != nil {
		return admissionRow{}, err
	}
	r.Timeout = time.Duration(timeout) * time.Millisecond
	r.Heavy, r.Engine, r.Ahead, r.Source.Trial = heavy == 1, engine == 1, ahead == 1, trial == 1
	r.Source.Number, r.Created = number, time.UnixMilli(created)
	return r, nil
}

// Acquire waits for the node to admit d and returns the port index the check
// may use on that node. The row is the wait: the engine's pass marks it
// admitted when the node has room, and note receives each new wait reason as
// the pass records it, so the holder can show why the work has not started. A
// context that ends stops the wait and releases the row.
func (a Admission) Acquire(ctx context.Context, d Demand, wait func(context.Context, time.Duration) error, note func(reason string)) (int, error) {
	if d.Node == "" || d.ID == "" {
		return -1, errors.New("admission needs a node and an ID")
	}
	if err := a.upsert(ctx, d, false); err != nil {
		return -1, err
	}
	if note == nil {
		note = func(string) {}
	}
	if wait == nil {
		wait = sleepContext
	}
	lastReason := ""
	for {
		// Whoever waits drives a pass too, so a queue with no engine watching
		// it still admits. A failed pass retries on the next tick.
		_, _, _ = a.Pass(ctx, nil)
		row, err := a.get(ctx, d.ID)
		if errors.Is(err, os.ErrNotExist) {
			// The row was swept while its holder still waits; queue again.
			if err := a.upsert(ctx, d, false); err != nil {
				return -1, err
			}
			continue
		}
		if err != nil {
			return -1, err
		}
		if row.State == "admitted" {
			return row.Port, nil
		}
		if row.Reason != lastReason {
			lastReason = row.Reason
			if row.Reason != "" {
				note(row.Reason)
			}
		}
		if err := wait(ctx, pollEvery); err != nil {
			_ = a.Release(ctx, d.Node, d.ID)
			return -1, err
		}
	}
}

// Hold reattaches running work while preserving its original port.
func (a Admission) Hold(ctx context.Context, d Demand) (int, error) {
	if d.Timeout <= 0 {
		d.Timeout = DefaultTimeout
	}
	db, err := a.db()
	if err != nil {
		return -1, err
	}
	port := -1
	err = state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
		row, err := scanAdmission(conn.QueryRowContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE id=?", d.ID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && row.State == "admitted" {
			if row.Node != d.Node {
				return fmt.Errorf("admission %s belongs to node %s", d.ID, row.Node)
			}
			port = row.Port
			_, err = conn.ExecContext(ctx, "UPDATE admissions SET timeout_ms=?, expires_ms=? WHERE id=?", d.Timeout.Milliseconds(), a.now().Add(d.Timeout+reservationMargin).UnixMilli(), d.ID)
			return err
		}
		rows, err := rowsConn(ctx, conn, "admitted")
		if err != nil {
			return err
		}
		port = lowestPort(rows[d.Node], d.ID)
		if port < 0 {
			return errors.New("node " + d.Node + " has no free check port")
		}
		pid, start := admissionOwner()
		if a.Engine {
			pid, start = 0, 0
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO admissions("+admissionColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET node=excluded.node, room=excluded.room, cpus=excluded.cpus, memory_mib=excluded.memory_mib, heavy=excluded.heavy, ahead=excluded.ahead, port=excluded.port, state='admitted', reason='', engine=excluded.engine, owner_pid=excluded.owner_pid, owner_start=excluded.owner_start, timeout_ms=excluded.timeout_ms, expires_ms=excluded.expires_ms, check_name=excluded.check_name, kind=excluded.kind, number=excluded.number, trial=excluded.trial, branch=excluded.branch, head=excluded.head",
			d.ID, d.Node, d.Room, d.CPUs, d.MemoryMiB, boolInt(d.Heavy), boolInt(d.Ahead), port, "admitted", "", boolInt(a.Engine), pid, start, d.Timeout.Milliseconds(), a.now().Add(d.Timeout+reservationMargin).UnixMilli(), a.now().UnixMilli(), d.Check, d.Source.Kind, d.Source.Number, boolInt(d.Source.Trial), d.Source.Branch, d.Source.Head)
		return err
	})
	return port, err
}

// Reattach recovers only an existing reservation. Allocating a new port would
// be unsafe because a detached check still uses its original port.
func (a Admission) Reattach(ctx context.Context, d Demand) (int, error) {
	if d.Timeout <= 0 {
		d.Timeout = DefaultTimeout
	}
	db, err := a.db()
	if err != nil {
		return -1, err
	}
	port := -1
	err = state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
		row, err := scanAdmission(conn.QueryRowContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE id=? AND node=? AND state='admitted'", d.ID, d.Node))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check %s on %s: %w", d.ID, d.Node, ErrNoReservation)
		}
		if err != nil {
			return err
		}
		port = row.Port
		pid, start := admissionOwner()
		if a.Engine {
			pid, start = 0, 0
		}
		_, err = conn.ExecContext(ctx, "UPDATE admissions SET timeout_ms=?, expires_ms=?, engine=?, owner_pid=?, owner_start=? WHERE id=?", d.Timeout.Milliseconds(), a.now().Add(d.Timeout+reservationMargin).UnixMilli(), boolInt(a.Engine), pid, start, d.ID)
		return err
	})
	return port, err
}

// Renew extends an admitted check's reservation while its node still reports
// it running. A missing reservation cannot safely be recreated with a new port.
func (a Admission) Renew(ctx context.Context, node, id string) error {
	db, err := a.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, "UPDATE admissions SET expires_ms=?+timeout_ms WHERE id=? AND node=? AND state='admitted'", a.now().Add(reservationMargin).UnixMilli(), id, node)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("reservation for check %s on %s is missing", id, node)
		}
		return nil
	})
}

// Release ends this holder's row. Its capacity and its port index return at
// the next pass.
func (a Admission) Release(ctx context.Context, node, id string) error {
	pid, start := admissionOwner()
	if a.Engine {
		return a.write(ctx, "UPDATE admissions SET state='released' WHERE id=? AND node=? AND engine=1", id, node)
	}
	return a.write(ctx, "UPDATE admissions SET state='released' WHERE id=? AND node=? AND owner_pid=? AND owner_start=?", id, node, pid, start)
}

// RunReservations lists the IDs of the admitted reservations of one local CI
// run. Every check of a run has a row ID that starts with the run ID. A row
// that a process outside this engine still holds is live work, so it is left
// out.
func (a Admission) RunReservations(ctx context.Context, runID string) ([]string, error) {
	db, err := a.db()
	if err != nil {
		return nil, err
	}
	prefix := runID + "-"
	rows, err := db.QueryContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE state='admitted' AND substr(id,1,?)=?", len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		row, err := scanAdmission(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if !row.Engine && ownerAlive(row.PID, row.Start) {
			continue
		}
		ids = append(ids, row.ID)
	}
	return ids, errors.Join(rows.Err(), rows.Close())
}

// ReleaseReservations ends the given admitted reservations. Their capacity and
// ports return at the next pass.
func (a Admission) ReleaseReservations(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := a.write(ctx, "UPDATE admissions SET state='released' WHERE id=? AND state='admitted'", id); err != nil {
			return err
		}
	}
	return nil
}

// ResetEngine removes abandoned waiters, but keeps detached check reservations.
// Those checks retain their capacity and ports until they finish or are stopped.
func (a Admission) ResetEngine(ctx context.Context) error {
	return a.write(ctx, "DELETE FROM admissions WHERE engine=1 AND state IN ('waiting','released')")
}

func (a Admission) get(ctx context.Context, id string) (admissionRow, error) {
	db, err := a.db()
	if err != nil {
		return admissionRow{}, err
	}
	_ = db // one shared handle per data folder; never closed
	row, err := scanAdmission(db.QueryRowContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return admissionRow{}, os.ErrNotExist
	}
	return row, err
}

func (a Admission) upsert(ctx context.Context, d Demand, admitted bool) error {
	if d.Timeout <= 0 {
		d.Timeout = DefaultTimeout
	}
	pid, start := admissionOwner()
	if a.Engine {
		pid, start = 0, 0
	}
	state := "waiting"
	if admitted {
		state = "admitted"
	}
	return a.write(ctx, "INSERT INTO admissions("+admissionColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET node=excluded.node, room=excluded.room, cpus=excluded.cpus, memory_mib=excluded.memory_mib, heavy=excluded.heavy, ahead=excluded.ahead, state=excluded.state, reason='', engine=excluded.engine, owner_pid=excluded.owner_pid, owner_start=excluded.owner_start, timeout_ms=excluded.timeout_ms, expires_ms=excluded.expires_ms, check_name=excluded.check_name, kind=excluded.kind, number=excluded.number, trial=excluded.trial, branch=excluded.branch, head=excluded.head",
		d.ID, d.Node, d.Room, d.CPUs, d.MemoryMiB, boolInt(d.Heavy), boolInt(d.Ahead), -1, state, "", boolInt(a.Engine), pid, start, d.Timeout.Milliseconds(), a.now().Add(d.Timeout+reservationMargin).UnixMilli(), a.now().UnixMilli(), d.Check, d.Source.Kind, d.Source.Number, boolInt(d.Source.Trial), d.Source.Branch, d.Source.Head)
}

// nodeQueue is one node's queue in one shape: the checks and jobs that wait
// for room on the node, and the work the node already holds. The pass builds
// it inside its transaction; the snapshot counts, the free-CPU figure and the
// machine page read it. Every projection of a node's work shapes this one
// result, so a new held dimension needs one edit, not one per projection.
type nodeQueue struct {
	waitingRows []admissionRow
	heldRows    []admissionRow
	running     []Job
	queued      []Job
}

// readQueue loads one node's queue from the admissions rows and the open
// jobs. It does not sweep; each reader sweeps for itself.
func (a Admission) readQueue(ctx context.Context, node string) (nodeQueue, error) {
	db, err := a.db()
	if err != nil {
		return nodeQueue{}, err
	}
	_ = db // one shared handle per data folder; never closed
	query, err := db.QueryContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE node=? AND state IN ('waiting','admitted') ORDER BY created_ms, id", node)
	if err != nil {
		return nodeQueue{}, err
	}
	var q nodeQueue
	for query.Next() {
		row, err := scanAdmission(query)
		if err != nil {
			query.Close()
			return nodeQueue{}, err
		}
		if row.State == "waiting" {
			q.waitingRows = append(q.waitingRows, row)
		} else {
			q.heldRows = append(q.heldRows, row)
		}
	}
	if err := errors.Join(query.Err(), query.Close()); err != nil {
		return nodeQueue{}, err
	}
	open, err := (Store{DataDir: a.DataDir}).NodeOpen(ctx, node)
	if err != nil {
		return nodeQueue{}, err
	}
	// NodeOpen answers oldest first, the order the running list shows.
	for _, job := range open {
		switch job.State {
		case StateRunning:
			q.running = append(q.running, job)
		case StateQueued:
			q.queued = append(q.queued, job)
		}
	}
	return q, nil
}

// waiters lists the node's waiting units in one slice: checks and jobs.
// Checks want a port; jobs carry the named resources they will hold.
func (q nodeQueue) waiters(limits machines.Machine) []waiter {
	out := make([]waiter, 0, len(q.waitingRows)+len(q.queued))
	for _, row := range q.waitingRows {
		out = append(out, waiter{demand: row.Demand, created: row.Created, id: row.ID, reason: row.Reason})
	}
	for _, job := range q.queued {
		out = append(out, waiter{demand: jobDemand(job, limits), resources: job.Resources, created: job.Created, id: job.ID, isJob: true})
	}
	return out
}

// holder names one unit that holds a node's capacity, for wait reasons and
// resource counts.
type holder struct {
	id   string
	room string
}

// heldWork is what one node's queue already holds: its running jobs and
// admitted check rows, counted over every dimension admission tracks. The
// pass adds each unit it admits; the counts and the machine page read the
// same totals, so no projection can disagree with the admission decision.
type heldWork struct {
	cpus      int
	memMiB    int64
	heavy     int
	portsUsed []bool
	resources map[machines.ResourceName][]holder
}

// hold counts the work the node's queue already holds. limits clamp each
// running job's demand the way the reader's own machine facts clamp it.
func (q nodeQueue) hold(limits machines.Machine) heldWork {
	held := heldWork{portsUsed: make([]bool, maxPortsPerNode), resources: map[machines.ResourceName][]holder{}}
	for _, job := range q.running {
		demand := jobDemand(job, limits)
		held.cpus += demand.CPUs
		held.memMiB += demand.MemoryMiB
		if demand.Heavy {
			held.heavy++
		}
		for _, name := range job.Resources {
			held.resources[name] = append(held.resources[name], holder{id: job.ID, room: job.RoomID})
		}
	}
	for _, row := range q.heldRows {
		held.cpus += row.CPUs
		held.memMiB += row.MemoryMiB
		if row.Heavy {
			held.heavy++
		}
		if row.Port >= 0 && row.Port < maxPortsPerNode {
			held.portsUsed[row.Port] = true
		}
	}
	return held
}

// waitingNodes lists the nodes that have at least one waiting row.
func (a Admission) waitingNodes(ctx context.Context) ([]string, error) {
	db, err := a.db()
	if err != nil {
		return nil, err
	}
	_ = db // one shared handle per data folder; never closed
	rows, err := db.QueryContext(ctx, "SELECT DISTINCT node FROM admissions WHERE state='waiting'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func (a Admission) write(ctx context.Context, query string, args ...any) error {
	db, err := a.db()
	if err != nil {
		return err
	}
	_ = db // one shared handle per data folder; never closed
	return state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, query, args...)
		return err
	})
}

// lowestPort is the first index none of the held rows uses.
func lowestPort(held []admissionRow, exceptID string) int {
	used := make([]bool, maxPortsPerNode)
	for _, row := range held {
		if row.ID != exceptID && row.Port >= 0 && row.Port < maxPortsPerNode {
			used[row.Port] = true
		}
	}
	for index, taken := range used {
		if !taken {
			return index
		}
	}
	return -1
}

// waiter is one waiting unit in a pass: a queued job or a waiting check row.
// A check also needs a port index; a job does not, so a waiter that is not a
// job wants a port.
type waiter struct {
	demand    Demand
	resources []machines.ResourceName
	created   time.Time
	id        string
	reason    string
	isJob     bool
}

// Pass decides from a read snapshot, then repeats any needed decision under
// the writer lock. A job becomes running before that transaction commits, so
// its capacity is reserved before its launch goroutine starts.
func (a Admission) Pass(ctx context.Context, candidates []Job) (map[string]bool, map[string]string, error) {
	// Resolve machine facts before taking the writer lock. A newly queued node
	// not in this snapshot is considered on the next pass.
	waiting, err := a.waitingNodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	nodes := map[string]bool{}
	for _, node := range waiting {
		nodes[node] = true
	}
	for _, job := range candidates {
		nodes[job.Node] = true
	}
	facts := map[string]machines.Machine{}
	factErrors := map[string]error{}
	for node := range nodes {
		facts[node], factErrors[node] = a.machines().Get(ctx, node)
	}
	if _, err := (Store{DataDir: a.DataDir}).db(); err != nil {
		return nil, nil, err
	}
	db, err := a.db()
	if err != nil {
		return nil, nil, err
	}
	admitted, reasons := map[string]bool{}, map[string]string{}
	decide := func(conn state.Reader, writer *sql.Conn) (bool, error) {
		needsSweep, err := sweepOn(ctx, conn, writer, a.now())
		if err != nil {
			return false, err
		}
		if needsSweep && writer == nil {
			return true, nil
		}
		needsWrite := false
		waiting, err := rowsConn(ctx, conn, "waiting")
		if err != nil {
			return false, err
		}
		held, err := rowsConn(ctx, conn, "admitted")
		if err != nil {
			return false, err
		}
		rows, err := conn.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='running'")
		if err != nil {
			return false, err
		}
		running := map[string][]Job{}
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				rows.Close()
				return false, err
			}
			running[job.Node] = append(running[job.Node], job)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return false, err
		}
		for node := range nodes {
			var queued []Job
			for _, candidate := range candidates {
				if candidate.Node != node {
					continue
				}
				job, err := scanJob(conn.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id=? AND state='queued' AND cancel_requested=0", candidate.ID))
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return false, err
				}
				queued = append(queued, job)
			}
			q := nodeQueue{waitingRows: waiting[node], heldRows: held[node], running: running[node], queued: queued}
			if err := passNode(ctx, writer, a.now(), node, facts[node], factErrors[node], q, a.RoomName, admitted, reasons, &needsWrite); err != nil {
				return false, err
			}
		}
		return needsWrite, nil
	}
	var needsWrite bool
	err = state.For(db).Read(ctx, func(reader state.Reader) error {
		var err error
		needsWrite, err = decide(reader, nil)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if needsWrite {
		admitted, reasons = map[string]bool{}, map[string]string{}
		err = state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
			_, err := decide(conn, conn)
			return err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return admitted, reasons, nil
}

// rowsConn reads queue rows on the same transaction as the admission decision.
func rowsConn(ctx context.Context, conn state.Reader, status string) (map[string][]admissionRow, error) {
	rows, err := conn.QueryContext(ctx, "SELECT "+admissionColumns+" FROM admissions WHERE state=? ORDER BY created_ms, id", status)
	if err != nil {
		return nil, err
	}
	out := map[string][]admissionRow{}
	for rows.Next() {
		row, err := scanAdmission(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[row.Node] = append(out[row.Node], row)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// sweepOn checks expiry and dead owners in the decision snapshot. When writer
// is present it rechecks and removes those rows under the writer lock.
func sweepOn(ctx context.Context, reader state.Reader, writer *sql.Conn, now time.Time) (bool, error) {
	var stale int
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM admissions WHERE state='released' OR (state='admitted' AND expires_ms<=?)", now.UnixMilli()).Scan(&stale); err != nil {
		return false, err
	}
	rows, err := reader.QueryContext(ctx, "SELECT id,owner_pid,owner_start FROM admissions WHERE engine=0 AND state='waiting'")
	if err != nil {
		return false, err
	}
	type deadRow struct {
		id    string
		pid   int
		start uint64
	}
	var dead []deadRow
	for rows.Next() {
		var row deadRow
		if err := rows.Scan(&row.id, &row.pid, &row.start); err != nil {
			rows.Close()
			return false, err
		}
		if !ownerAlive(row.pid, row.start) {
			dead = append(dead, row)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	needed := stale != 0 || len(dead) != 0
	if !needed || writer == nil {
		return needed, nil
	}
	if _, err := writer.ExecContext(ctx, "DELETE FROM admissions WHERE state='released' OR (state='admitted' AND expires_ms<=?)", now.UnixMilli()); err != nil {
		return false, err
	}
	for _, row := range dead {
		if _, err := writer.ExecContext(ctx, "DELETE FROM admissions WHERE id=? AND state='waiting' AND engine=0 AND owner_pid=? AND owner_start=?", row.id, row.pid, row.start); err != nil {
			return false, err
		}
	}
	return true, nil
}

// sweep deletes released and expired rows, and waiting rows whose outside
// holder is gone. A detached check can outlive its outside holder, so its
// admitted row remains until release or expiry.
func (a Admission) sweep(ctx context.Context) error {
	db, err := a.db()
	if err != nil {
		return err
	}
	var needed bool
	err = state.For(db).Read(ctx, func(reader state.Reader) error {
		var err error
		needed, err = sweepOn(ctx, reader, nil, a.now())
		return err
	})
	if err != nil || !needed {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: AdmissionTopic}, func(conn *sql.Conn) error {
		_, err := sweepOn(ctx, conn, conn, a.now())
		return err
	})
}

// passNode decides the waiting work of one node. A nil conn checks for changes
// without writing. Held work is the running jobs plus admitted rows; capacity,
// limits and disk use come from the machine's last probe and stored limits.
func passNode(ctx context.Context, conn *sql.Conn, now time.Time, node string, machine machines.Machine, machineErr error, q nodeQueue, roomName func(string) string, admitted map[string]bool, reasons map[string]string, needsWrite *bool) error {
	capacityCPUs, capacityMemMiB := 0, int64(0)
	limits := machines.Machine{}
	if machineErr == nil {
		capacityCPUs, capacityMemMiB = machine.Capabilities.CPUs, int64(machine.Capabilities.MemTotal/(1<<20))
		limits = machine
	}
	// Every waiting unit of this node in one order, and the work the node
	// already holds: what runs now and what an earlier pass admitted.
	ordered := orderWaiters(q.waiters(limits))
	held := q.hold(limits)
	diskBlock := ""
	if machineErr == nil {
		diskBlock = machine.DiskBlock()
	}
	var writes []rowWrite
	for index, unit := range ordered {
		d := unit.demand
		freeCPUs, freeMemMiB := 0, int64(0)
		if capacityCPUs > 0 {
			freeCPUs = capacityCPUs - held.cpus
		}
		if capacityMemMiB > 0 {
			freeMemMiB = capacityMemMiB - held.memMiB
		}
		port := -1
		if !unit.isJob {
			for candidate, taken := range held.portsUsed {
				if !taken {
					port = candidate
					break
				}
			}
		}
		resourceBlock := ""
		for _, name := range unit.resources {
			limit := limits.Resources[name]
			if limit == 0 || isWorkspaceResource(name) {
				// An undeclared name is a lock with one unit. A workspace's
				// internal name always stays a one-unit lock.
				limit = 1
			}
			holders := held.resources[name]
			if len(holders) < limit {
				continue
			}
			holder := holders[0]
			room := holder.room
			if roomName != nil {
				if resolved := roomName(room); resolved != "" {
					room = resolved
				}
			}
			if isWorkspaceResource(name) {
				resourceBlock = "workspace held by " + holder.id
			} else {
				resourceBlock = fmt.Sprintf("resource %s held by %s", name, holder.id)
			}
			if room != "" {
				resourceBlock += fmt.Sprintf(" (room %s)", room)
			}
			break
		}
		var why string
		switch {
		case machineErr != nil:
			why = "node " + node + " is not registered"
		case diskBlock != "":
			why = diskBlock
		case resourceBlock != "":
			why = resourceBlock
		case d.Heavy && held.heavy >= limits.IOHeavyLimit():
			why = fmt.Sprintf("%d of %d IO-heavy jobs running", held.heavy, limits.IOHeavyLimit())
		case capacityCPUs > 0 && d.CPUs > freeCPUs:
			why = fmt.Sprintf("asks %d CPUs, %d free", d.CPUs, max(freeCPUs, 0))
		case capacityMemMiB > 0 && d.MemoryMiB > freeMemMiB:
			why = fmt.Sprintf("asks %s, %s free", MemoryLabel(d.MemoryMiB), MemoryLabel(max(freeMemMiB, 0)))
		case !unit.isJob && port < 0:
			why = "all check ports in use"
		}
		if why == "" {
			held.cpus += d.CPUs
			held.memMiB += d.MemoryMiB
			if d.Heavy {
				held.heavy++
			}
			*needsWrite = true
			if unit.isJob {
				if conn == nil {
					admitted[unit.id] = true
					for _, name := range unit.resources {
						held.resources[name] = append(held.resources[name], holder{id: unit.id, room: d.Room})
					}
					continue
				}
				result, err := conn.ExecContext(ctx, "UPDATE jobs SET state='running', started_ms=? WHERE id=? AND state='queued' AND cancel_requested=0", time.Now().UnixMilli(), unit.id)
				if err != nil {
					return err
				}
				count, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if count == 1 {
					admitted[unit.id] = true
					for _, name := range unit.resources {
						held.resources[name] = append(held.resources[name], holder{id: unit.id, room: d.Room})
					}
				}
			} else {
				held.portsUsed[port] = true
				writes = append(writes, rowWrite{id: unit.id, port: port})
			}
			continue
		}
		reason := waitingText(why, index+1, len(ordered))
		if unit.isJob {
			reasons[unit.id] = reason
		} else if unit.reason != reason {
			*needsWrite = true
			writes = append(writes, rowWrite{id: unit.id, reason: reason})
		}
	}
	if conn == nil {
		return nil
	}
	for _, write := range writes {
		if write.reason == "" {
			if _, err := conn.ExecContext(ctx, "UPDATE admissions SET state='admitted', port=?, reason='', expires_ms=?+timeout_ms WHERE id=? AND state='waiting'", write.port, now.Add(reservationMargin).UnixMilli(), write.id); err != nil {
				return err
			}
		} else if _, err := conn.ExecContext(ctx, "UPDATE admissions SET reason=? WHERE id=? AND state='waiting'", write.reason, write.id); err != nil {
			return err
		}
	}
	return nil
}

// rowWrite is one queued-row write of a pass: admit with a port, or record
// the wait reason.
type rowWrite struct {
	id     string
	port   int
	reason string
}

// waitingText words one wait reason with the unit's place in the node's queue.
func waitingText(why string, position, total int) string {
	if why == "" {
		return fmt.Sprintf("waiting: position %d of %d", position, total)
	}
	return fmt.Sprintf("waiting: %s, position %d of %d", why, position, total)
}

// jobDemand reads a job's clamped demand. A machine with no probe keeps the
// job's own request, so an unprobed node still takes work.
func jobDemand(job Job, machine machines.Machine) Demand {
	resources := ClampResources(job.Host, job.CPUs, job.MemoryMiB, Facts{CPUs: machine.Capabilities.CPUs, MemoryBytes: machine.Capabilities.MemTotal})
	return Demand{Node: job.Node, ID: job.ID, Room: job.RoomID, Heavy: job.Heavy, CPUs: resources.CPUs, MemoryMiB: resources.MemoryMiB}
}

// orderWaiters interleaves rooms: each room's waiters oldest first, rooms in
// the order of their oldest waiter, one from each room in turn, so one room
// cannot fill a node while another waits behind it.
func orderWaiters(waiters []waiter) []waiter {
	ordered := append([]waiter(nil), waiters...)
	slices.SortStableFunc(ordered, func(a, b waiter) int {
		if !a.created.Equal(b.created) {
			if a.created.Before(b.created) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
	var rooms []string
	byRoom := map[string][]waiter{}
	for _, unit := range ordered {
		if _, seen := byRoom[unit.demand.Room]; !seen {
			rooms = append(rooms, unit.demand.Room)
		}
		byRoom[unit.demand.Room] = append(byRoom[unit.demand.Room], unit)
	}
	// A room's ahead work, a merge trial's checks, lines up in front of that
	// room's other waiters, so the merge queue it holds never waits behind its
	// own room's pull-request checks. Each list stays oldest first, rooms keep
	// taking turns, and jobs keep their order.
	for room, units := range byRoom {
		ahead := make([]waiter, 0, len(units))
		rest := make([]waiter, 0, len(units))
		for _, unit := range units {
			if unit.demand.Ahead {
				ahead = append(ahead, unit)
			} else {
				rest = append(rest, unit)
			}
		}
		byRoom[room] = append(ahead, rest...)
	}
	out := make([]waiter, 0, len(ordered))
	for round := 0; len(out) < len(ordered); round++ {
		for _, room := range rooms {
			if round < len(byRoom[room]) {
				out = append(out, byRoom[room][round])
			}
		}
	}
	return out
}

// Snapshot counts the work one node runs now and the work that waits for
// room on it, checks and jobs both. A node's page reads it, so the queue is
// visible without reading a log.
func (a Admission) Snapshot(ctx context.Context, node string) (running, waiting int, err error) {
	if err := a.sweep(ctx); err != nil {
		return 0, 0, err
	}
	q, err := a.readQueue(ctx, node)
	if err != nil {
		return 0, 0, err
	}
	return len(q.heldRows) + len(q.running), len(q.waitingRows) + len(q.queued), nil
}

// FreeCPUs reports the CPUs a node has free after the work its queue holds:
// waiting and admitted rows, and running jobs. capacity is the node's probed
// CPU count, which the caller reads from its node view; a node whose probe
// read no CPU count counts as unlimited rather than closed.
func (a Admission) FreeCPUs(ctx context.Context, node string, capacity int) int {
	if capacity <= 0 {
		// No probe read the node, so its queue counts nothing against it.
		return unlimitedCPUs
	}
	if err := a.sweep(ctx); err != nil {
		return 0
	}
	q, err := a.readQueue(ctx, node)
	if err != nil {
		return capacity
	}
	limits := machines.Machine{}
	limits.Capabilities.CPUs = capacity
	// Waiting checks count too: each takes its CPUs as soon as the node has
	// room for it.
	used := q.hold(limits).cpus
	for _, row := range q.waitingRows {
		used += row.CPUs
	}
	for _, job := range q.queued {
		used += jobDemand(job, limits).CPUs
	}
	return max(capacity-used, 0)
}
