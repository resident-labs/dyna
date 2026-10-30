package activity

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Kind names the execution that owns an activity record.
type Kind string

const (
	Turn        Kind = "turn"
	CommandStep Kind = "command-step"
	AgentStep   Kind = "agent-step"
)

// Key identifies one turn or workflow step. Number is scoped to Owner.
type Key struct {
	Kind   Kind
	Owner  string
	Number int
}

// UnknownReason names a boundary that cannot observe all relevant output.
type UnknownReason string

const (
	ReasonNone                     UnknownReason = ""
	ReasonStderrUnobserved         UnknownReason = "stderr-unobserved"
	ReasonPartialFrameUnobserved   UnknownReason = "partial-frame-unobserved"
	ReasonOutputUnobserved         UnknownReason = "output-unobserved"
	ReasonObserverLivenessUnproven UnknownReason = "observer-liveness-unproven"
)

// Record links a workflow step to the shell or session that produces output.
type Record struct {
	Started   time.Time
	Signal    Signal
	Lifecycle CopierState
	Reason    UnknownReason
	Shell     string
	Session   string
}

func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "activity.version", 2, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), `CREATE TABLE activity_records (
			kind TEXT NOT NULL, owner TEXT NOT NULL, number INTEGER NOT NULL,
			last_output INTEGER NOT NULL DEFAULT 0, lease INTEGER NOT NULL DEFAULT 0,
			copier TEXT NOT NULL DEFAULT '', cpu TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '', shell TEXT NOT NULL DEFAULT '', session TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(kind, owner, number))`)
			if err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(context.Background(), `ALTER TABLE activity_records ADD COLUMN started INTEGER NOT NULL DEFAULT 0; ALTER TABLE activity_records ADD COLUMN observed_since INTEGER NOT NULL DEFAULT 0`)
		return err
	})
}

// Put saves one observation. The caller owns bounded asynchronous publication.
func Put(ctx context.Context, db *sql.DB, key Key, rec Record) error {
	var last, lease, started, since int64
	if !rec.Started.IsZero() {
		started = rec.Started.UnixNano()
	}
	if !rec.Signal.ObservedSince.IsZero() {
		since = rec.Signal.ObservedSince.UnixNano()
	}
	if !rec.Signal.LastOutput.IsZero() {
		last = rec.Signal.LastOutput.UnixNano()
	}
	if !rec.Signal.LeaseUntil.IsZero() {
		lease = rec.Signal.LeaseUntil.UnixNano()
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO activity_records(kind,owner,number,last_output,lease,copier,cpu,lifecycle,reason,shell,session,started,observed_since)
		 SELECT ?,?,?,?,?,?,?,?,?,?,?,?,? WHERE (?='turn' AND EXISTS (SELECT 1 FROM session_records WHERE id=?)) OR (?!='turn' AND EXISTS (SELECT 1 FROM runs WHERE id=?))
		 ON CONFLICT(kind,owner,number) DO UPDATE SET
		 last_output=excluded.last_output,lease=excluded.lease,copier=excluded.copier,
		 cpu=excluded.cpu,lifecycle=excluded.lifecycle,reason=excluded.reason,shell=excluded.shell,session=excluded.session,started=excluded.started,observed_since=excluded.observed_since`,
			key.Kind, key.Owner, key.Number, last, lease, rec.Signal.Copier, rec.Signal.CPU, rec.Lifecycle, rec.Reason, rec.Shell, rec.Session, started, since, key.Kind, key.Owner, key.Kind, key.Owner)
		return err
	})
}

func Read(db *sql.DB, key Key, now time.Time) (Record, error) {
	var r Record
	var last, lease, started, since int64
	err := db.QueryRow(`SELECT last_output,lease,copier,cpu,lifecycle,reason,shell,session,started,observed_since FROM activity_records WHERE kind=? AND owner=? AND number=?`,
		key.Kind, key.Owner, key.Number).Scan(&last, &lease, &r.Signal.Copier, &r.Signal.CPU, &r.Lifecycle, &r.Reason, &r.Shell, &r.Session, &started, &since)
	if err == sql.ErrNoRows {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, err
	}
	if started != 0 {
		r.Started = time.Unix(0, started)
	}
	if since != 0 {
		r.Signal.ObservedSince = time.Unix(0, since)
	}
	if last != 0 {
		r.Signal.LastOutput = time.Unix(0, last)
	}
	if lease != 0 {
		r.Signal.LeaseUntil = time.Unix(0, lease)
	}
	r.Signal = r.Signal.At(now)
	if r.Signal.LeaseUntil.IsZero() || !now.Before(r.Signal.LeaseUntil) {
		r.Lifecycle = CopierUnknown
	}
	return r, nil
}

// DeleteOwner removes records inside the owner's deletion transaction. The
// activity table may be absent in isolated stores that did not run engine migrations.
func DeleteOwner(ctx context.Context, conn *sql.Conn, kind Kind, owner string) error {
	var found int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='activity_records'`).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return nil
	}
	if kind == Turn {
		_, err := conn.ExecContext(ctx, `DELETE FROM activity_records WHERE kind=? AND owner=?`, Turn, owner)
		return err
	}
	_, err := conn.ExecContext(ctx, `DELETE FROM activity_records WHERE kind IN (?,?) AND owner=?`, CommandStep, AgentStep, owner)
	return err
}
