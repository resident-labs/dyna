package localci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/procident"
	"dyna-agent/internal/state"
)

// attachmentTTL is how long a machine keeps a run's folder after the engine that
// started it left: the run's keeper removes the folder after three hours. A
// record older than that points at nothing.
const attachmentTTL = 3 * time.Hour

// attachmentCheckKind records which teardown a check needs. A legacy row
// without this metadata keeps the conservative teardown of every check type.
type attachmentCheckKind struct {
	Host      bool `json:"host,omitempty"`
	Container bool `json:"container,omitempty"`
	Job       bool `json:"job,omitempty"`
}

// attachment is what a restarted engine needs to follow the checks a run
// started on a machine: the run, and the checks that were already launched.
// The folders and the unit names derive from them. Source and CheckKinds stay
// in this JSON payload so older engines can read the same table.
type attachment struct {
	RunID      string                        `json:"runID"`
	Checks     []Suite                       `json:"checks"`
	Source     jobs.CheckSource              `json:"source"`
	CheckKinds map[Suite]attachmentCheckKind `json:"checkKinds,omitempty"`
}

func (a attachment) has(id Suite) bool { return slices.Contains(a.Checks, id) }

// attachmentOwner names the process that executes a run: its id and start time,
// so a reused process id is not taken for it. Tests replace it.
var attachmentOwner = func() (pid int, start uint64) {
	pid = os.Getpid()
	start, _ = procident.StartTime(pid)
	return pid, start
}

// ownerAlive reports whether the process that owned a run still runs. Tests
// replace it.
var ownerAlive = func(pid int, start uint64) bool {
	if pid <= 0 || !procident.Alive(pid) {
		return false
	}
	got, err := procident.StartTime(pid)
	return err != nil || got == start
}

// OrphanedAttachment identifies a run whose owner process is gone.
type OrphanedAttachment struct {
	Repo, SHA, Node, RunID string
	Source                 jobs.CheckSource
}

// attachmentRow is one stored run record and its owner.
type attachmentRow struct {
	attachment
	Repo, SHA, Node string
	ownerPID        int
	ownerStart      uint64
	saved           time.Time
}

func (r attachmentRow) orphaned() bool {
	return !ownerAlive(r.ownerPID, r.ownerStart) && resultClock().Sub(r.saved) <= attachmentTTL
}

// saveAttachment records the started checks of a run on a machine, owned by this
// process.
func (s Store) saveAttachment(ctx context.Context, repo, sha, node string, att attachment) error {
	raw, err := json.Marshal(att)
	if err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	pid, start := attachmentOwner()
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: AttachEventKey(sha).String()}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, attachmentSchema); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO local_ci_attachments(repo,sha,node,run_id,payload,owner_pid,owner_start,saved_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(repo,sha,node,run_id) DO UPDATE SET payload=excluded.payload, owner_pid=excluded.owner_pid, owner_start=excluded.owner_start, saved_at=excluded.saved_at`, repo, sha, node, att.RunID, raw, pid, start, resultClock().Unix())
		return err
	})
}

// releaseAttachment hands a run's record over: the checks stay on the machine
// and the next run of the commit may take the record.
func (s Store) releaseAttachment(ctx context.Context, repo, sha, node, runID string) error {
	return s.updateAttachments(ctx, sha, "UPDATE local_ci_attachments SET owner_pid=0, owner_start=0 WHERE repo=? AND sha=? AND node=? AND run_id=?", repo, sha, node, runID)
}

// deleteAttachment forgets a run's checks once the run ended for good.
func (s Store) deleteAttachment(ctx context.Context, repo, sha, node, runID string) error {
	return s.updateAttachments(ctx, sha, "DELETE FROM local_ci_attachments WHERE repo=? AND sha=? AND node=? AND run_id=?", repo, sha, node, runID)
}

func (s Store) updateAttachments(ctx context.Context, sha, query string, args ...any) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: AttachEventKey(sha).String()}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, query, args...)
		if isMissingAttachmentTable(err) {
			return nil
		}
		return err
	})
}

// isMissingAttachmentTable reports the error of a database that has not
// recorded a check yet.
func isMissingAttachmentTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: local_ci_attachments")
}

// attachmentRows lists the records of a commit, newest first.
func (s Store) attachmentRows(ctx context.Context, repo, sha string) ([]attachmentRow, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT node, payload, owner_pid, owner_start, saved_at FROM local_ci_attachments WHERE repo=? AND sha=? ORDER BY saved_at DESC", repo, sha)
	if isMissingAttachmentTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []attachmentRow
	for rows.Next() {
		row := attachmentRow{Repo: repo, SHA: sha}
		var raw []byte
		var saved int64
		if err := rows.Scan(&row.Node, &raw, &row.ownerPID, &row.ownerStart, &saved); err != nil {
			rows.Close()
			return nil, err
		}
		row.saved = time.Unix(saved, 0)
		if err := json.Unmarshal(raw, &row.attachment); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// OrphanedAttachments lists every kept run whose owner process is gone.
func (s Store) OrphanedAttachments(ctx context.Context) ([]OrphanedAttachment, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT repo, sha, node, payload, owner_pid, owner_start, saved_at FROM local_ci_attachments ORDER BY saved_at DESC")
	if isMissingAttachmentTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OrphanedAttachment
	for rows.Next() {
		var row attachmentRow
		var raw []byte
		var saved int64
		if err := rows.Scan(&row.Repo, &row.SHA, &row.Node, &raw, &row.ownerPID, &row.ownerStart, &saved); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &row.attachment); err != nil {
			rows.Close()
			return nil, err
		}
		row.saved = time.Unix(saved, 0)
		if row.orphaned() {
			out = append(out, OrphanedAttachment{Repo: row.Repo, SHA: row.SHA, Node: row.Node, RunID: row.RunID, Source: row.Source})
		}
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// AbandonOrphanedRun finds the registered machine and tears down one kept run.
func (s Store) AbandonOrphanedRun(ctx context.Context, orphan OrphanedAttachment) error {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return err
	}
	for i := range nodes {
		if nodes[i].Name == orphan.Node {
			return (Runner{DataDir: s.DataDir, Node: &nodes[i], Engine: true}).AbandonOrphanedRun(ctx, s, orphan)
		}
	}
	return fmt.Errorf("abandon local CI run %s: node %s: %w", orphan.RunID, orphan.Node, os.ErrNotExist)
}

// claimAttachment takes over the newest record of a commit on a machine whose run
// has no live process, and returns it. It returns os.ErrNotExist when there is
// none. A record of a run that another engine or command is executing is not
// taken. The take-over is one conditional write, so two processes never both
// win.
func (s Store) claimAttachment(ctx context.Context, repo, sha, node string) (attachment, error) {
	rows, err := s.attachmentRows(ctx, repo, sha)
	if err != nil {
		return attachment{}, err
	}
	db, err := s.open()
	if err != nil {
		return attachment{}, err
	}
	pid, start := attachmentOwner()
	for _, row := range rows {
		if row.Node != node || !row.orphaned() {
			continue
		}
		taken := false
		err := state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: AttachEventKey(sha).String()}, func(conn *sql.Conn) error {
			res, err := conn.ExecContext(ctx, "UPDATE local_ci_attachments SET owner_pid=?, owner_start=?, saved_at=? WHERE repo=? AND sha=? AND node=? AND run_id=? AND owner_pid=? AND owner_start=?",
				pid, start, resultClock().Unix(), repo, sha, node, row.RunID, row.ownerPID, row.ownerStart)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			taken = n == 1
			return nil
		})
		if err != nil {
			return attachment{}, err
		}
		if taken {
			return row.attachment, nil
		}
	}
	return attachment{}, os.ErrNotExist
}

// AttachedNode names the machine that still holds checks of an earlier engine for
// this commit, so the scheduler places the run there. It returns an empty name
// when there is none. A record whose run another process executes is not a
// candidate.
func (s Store) AttachedNode(ctx context.Context, repo, sha string) (string, error) {
	rows, err := s.attachmentRows(ctx, repo, sha)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.orphaned() {
			return row.Node, nil
		}
	}
	return "", nil
}

// AbandonOrphanedRun claims one kept run and tears it down. A failed machine
// call hands the record back so the next scheduler tick can try again.
func (r Runner) AbandonOrphanedRun(ctx context.Context, store Store, orphan OrphanedAttachment) error {
	att, err := store.claimAttachment(ctx, orphan.Repo, orphan.SHA, orphan.Node)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim orphaned local CI run %s on %s: %w", orphan.RunID, orphan.Node, err)
	}
	cache := path.Join(cacheBase(orphan.Node), repoKey(orphan.Repo)+".git")
	return r.teardownAttachment(ctx, store, orphan.Repo, orphan.SHA, cache, att, nil)
}

// resumeRun finds the checks an earlier engine left on this machine for the same
// commit and takes the record over. It asks the machine once. A check that was
// recorded but never launched has no folder and starts afresh in the resumed
// run. When every launched check still has a unit or an exit record, the run
// reattaches to them. When one is gone, such as after a reboot, the run cannot be
// followed: everything it left on the machine is torn down first, and only when
// that succeeds are the folder and the record removed and the run started afresh.
// A machine that does not answer fails the run with the ssh-drop cause, and the
// record stays for the next try.
func (r Runner) resumeRun(ctx context.Context, store Store, repo, sha, cache string, checks []nodeCheck) (*attachment, error) {
	att, err := store.claimAttachment(ctx, repo, sha, r.Node.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		log.Printf("local CI %s: read started checks: %v", r.Node.Name, err)
		return nil, nil
	}
	giveBack := func() {
		if e := store.releaseAttachment(context.WithoutCancel(ctx), repo, sha, r.Node.Name, att.RunID); e != nil {
			log.Printf("local CI %s: hand back started checks: %v", r.Node.Name, e)
		}
	}
	original := att
	base := cacheBase(r.Node.Name)
	runDir := path.Join(base, "runs", att.RunID)
	stale := len(att.Checks) == 0
	var specs []jobs.RunSpec
	for _, id := range att.Checks {
		if !slices.ContainsFunc(checks, func(c nodeCheck) bool { return c.id == id }) {
			stale = true
		}
		specs = append(specs, jobs.RunSpec{ID: string(id), Dir: detachedDir(runDir, id), Unit: checkUnit(att.RunID, id)})
	}
	if !stale {
		frames, err := r.exec().Poll(ctx, specs, nil)
		if err != nil {
			giveBack()
			return nil, fmt.Errorf("reattach to the run of %s on %s: %w", sha[:12], r.Node.Name, err)
		}
		launched := attachment{RunID: att.RunID, Source: att.Source, CheckKinds: att.CheckKinds}
		var missing []string
		for _, spec := range specs {
			switch frames[spec.Dir].Unit {
			case jobs.UnitRunning, jobs.UnitExited:
				launched.Checks = append(launched.Checks, Suite(spec.ID))
			default:
				// Gone without an exit record: the run cannot be followed. A check
				// without a folder was never launched and is simply not attached.
				if frames[spec.Dir].Unit == jobs.UnitMissing {
					missing = append(missing, spec.ID)
				} else {
					stale = true
				}
			}
		}
		stale = stale || len(launched.Checks) == 0
		if !stale {
			for _, id := range missing {
				if err := r.queue().Release(ctx, r.Node.Name, att.RunID+"-"+id); err != nil {
					giveBack()
					return nil, fmt.Errorf("release unlaunched check %s: %w", id, err)
				}
			}
		}
		att = launched
	}
	if !stale {
		return &att, nil
	}
	if err := r.teardownAttachment(ctx, store, repo, sha, cache, original, checks); err != nil {
		return nil, err
	}
	return nil, nil
}

// teardownAttachment stops every recorded check, removes the run folder,
// releases its queue reservations and forgets its attachment. It hands the
// record back after a failure so a later run or scheduler tick can retry.
func (r Runner) teardownAttachment(ctx context.Context, store Store, repo, sha, cache string, att attachment, checks []nodeCheck) error {
	giveBack := func() {
		if e := store.releaseAttachment(context.WithoutCancel(ctx), repo, sha, r.Node.Name, att.RunID); e != nil {
			log.Printf("local CI %s: hand back started checks: %v", r.Node.Name, e)
		}
	}
	base := cacheBase(r.Node.Name)
	runDir := path.Join(base, "runs", att.RunID)
	specs := make([]jobs.RunSpec, 0, len(att.Checks))
	var recorded []nodeCheck
	for _, id := range att.Checks {
		specs = append(specs, jobs.RunSpec{ID: string(id), Dir: detachedDir(runDir, id), Unit: checkUnit(att.RunID, id)})
		if i := slices.IndexFunc(checks, func(c nodeCheck) bool { return c.id == id }); i >= 0 {
			recorded = append(recorded, checks[i])
			continue
		}
		kind, known := att.CheckKinds[id]
		check := nodeCheck{id: id, host: kind.Host}
		if known && kind.Container {
			check.image = "recorded"
		}
		if known && kind.Job {
			check.job = &JobRef{}
		}
		recorded = append(recorded, check)
	}
	// Tear down everything the old run left, containers and host scopes included,
	// and remove the folder only after that worked: a sibling that still runs must
	// not lose its workspace and its cache protection underneath it. The run's
	// keeper removes the folder when its own lifetime ends; the removal here only
	// speeds that up.
	var out tailBuffer
	if e := r.nodeCommand(ctx, &out, &out, teardownScript(*r.Node, cache, runDir, att.RunID, recorded)); e != nil {
		giveBack()
		return fmt.Errorf("stop the checks of the old run on %s: %w: %s", r.Node.Name, e, out.String())
	}
	if e := r.nodeCommand(ctx, &out, &out, removeRunDir(base, path.Join(runDir, ".active"))); e != nil {
		giveBack()
		return fmt.Errorf("remove the old run on %s: %w: %s", r.Node.Name, e, out.String())
	}
	// Teardown stopped every old check, so their ports and capacity are free.
	for _, spec := range specs {
		if err := r.queue().Release(ctx, r.Node.Name, att.RunID+"-"+spec.ID); err != nil {
			giveBack()
			return fmt.Errorf("release stopped check %s: %w", spec.ID, err)
		}
	}
	if err := store.deleteAttachment(ctx, repo, sha, r.Node.Name, att.RunID); err != nil {
		log.Printf("local CI %s: forget started checks: %v", r.Node.Name, err)
	}
	return nil
}
