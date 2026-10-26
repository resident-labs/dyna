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
	"sync"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/state"
)

// attachmentTTL is how long a machine keeps a run's folder after the engine that
// started it left: the run's keeper removes the folder after three hours. A
// record older than that points at nothing.
const attachmentTTL = 3 * time.Hour

// attachment is what a restarted engine needs to follow the checks a run
// started on a machine: the run, and the checks that were already launched.
// The folders and the unit names derive from them.
type attachment struct {
	RunID  string  `json:"runID"`
	Checks []Suite `json:"checks"`
}

func (a attachment) has(id Suite) bool { return slices.Contains(a.Checks, id) }

// saveAttachment records the started checks of a run on a machine.
func (s Store) saveAttachment(ctx context.Context, repo, sha, node string, att attachment) error {
	raw, err := json.Marshal(att)
	if err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "attach:" + sha}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, attachmentSchema); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO local_ci_attachments(repo,sha,node,payload,saved_at) VALUES(?,?,?,?,?)
ON CONFLICT(repo,sha,node) DO UPDATE SET payload=excluded.payload, saved_at=excluded.saved_at`, repo, sha, node, raw, resultClock().Unix())
		return err
	})
}

// loadAttachment reads the checks a run left running on a machine. It returns
// os.ErrNotExist when there is none, or when the record is older than the
// machine keeps the run.
func (s Store) loadAttachment(ctx context.Context, repo, sha, node string) (attachment, error) {
	var att attachment
	db, err := s.open()
	if err != nil {
		return att, err
	}
	var raw []byte
	var saved int64
	err = db.QueryRowContext(ctx, "SELECT payload, saved_at FROM local_ci_attachments WHERE repo=? AND sha=? AND node=?", repo, sha, node).Scan(&raw, &saved)
	if errors.Is(err, sql.ErrNoRows) || isMissingAttachmentTable(err) {
		return att, os.ErrNotExist
	}
	if err != nil {
		return att, err
	}
	if resultClock().Sub(time.Unix(saved, 0)) > attachmentTTL {
		return att, os.ErrNotExist
	}
	return att, json.Unmarshal(raw, &att)
}

// isMissingAttachmentTable reports the error of a database that has not
// recorded a check yet.
func isMissingAttachmentTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: local_ci_attachments")
}

// deleteAttachment forgets a run's checks once the run ended for good.
func (s Store) deleteAttachment(ctx context.Context, repo, sha, node string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "attach:" + sha}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM local_ci_attachments WHERE repo=? AND sha=? AND node=?", repo, sha, node)
		if isMissingAttachmentTable(err) {
			return nil
		}
		return err
	})
}

// liveRuns holds the runs this engine is executing now, so a second run of the
// same commit on the same machine leaves the first one's attachment alone.
var liveRuns sync.Map

// claimRun marks a run as live. It returns false when another run of this engine
// already holds the key. The attachment of a run that is not live belongs to an
// earlier engine, and the next run may take it over.
func claimRun(key string) (release func(), claimed bool) {
	if _, held := liveRuns.LoadOrStore(key, struct{}{}); held {
		return func() {}, false
	}
	return func() { liveRuns.Delete(key) }, true
}

// AttachedNode names the machine that still holds checks of an earlier engine for
// this commit, so the scheduler places the run there. It returns an empty name
// when there is none.
func (s Store) AttachedNode(ctx context.Context, repo, sha string) (string, error) {
	db, err := s.open()
	if err != nil {
		return "", err
	}
	var node string
	err = db.QueryRowContext(ctx, "SELECT node FROM local_ci_attachments WHERE repo=? AND sha=? AND saved_at>=? ORDER BY saved_at DESC LIMIT 1", repo, sha, resultClock().Add(-attachmentTTL).Unix()).Scan(&node)
	if errors.Is(err, sql.ErrNoRows) || isMissingAttachmentTable(err) {
		return "", nil
	}
	return node, err
}

// resumeRun finds the checks an earlier engine left on this machine for the same
// commit. It asks the machine once. A check that was recorded but never
// launched has no folder and starts afresh in the resumed run. When every
// launched check still has a unit or an exit record, the run reattaches to them.
// When one is gone, such as after a reboot, the run cannot be followed: every
// recorded unit is stopped, the folder is removed, the record is forgotten, and
// the run starts afresh. A machine that does not answer fails the run with the
// ssh-drop cause, and the record stays for the next try.
func (r Runner) resumeRun(ctx context.Context, store Store, repo, sha string, checks []nodeCheck) (*attachment, error) {
	att, err := store.loadAttachment(ctx, repo, sha, r.Node.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		log.Printf("local CI %s: read started checks: %v", r.Node.Name, err)
		return nil, nil
	}
	base := cacheBase(r.Node.Name)
	runDir := path.Join(base, "runs", att.RunID)
	stale := len(att.Checks) == 0
	var batch []*watch
	for _, id := range att.Checks {
		if !slices.ContainsFunc(checks, func(c nodeCheck) bool { return c.id == id }) {
			stale = true
		}
		batch = append(batch, &watch{spec: detachSpec{ID: id, Dir: detachedDir(runDir, id), Unit: checkUnit(att.RunID, id)}})
	}
	if !stale {
		frames, err := r.pollNode(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("reattach to the run of %s on %s: %w", sha[:12], r.Node.Name, err)
		}
		launched := attachment{RunID: att.RunID}
		for _, w := range batch {
			switch frames[w.spec.Dir].state {
			case pollRunning, pollExited:
				launched.Checks = append(launched.Checks, w.spec.ID)
			default:
				// Gone without an exit record: the run cannot be followed. A check
				// without a folder was never launched and is simply not attached.
				stale = stale || frames[w.spec.Dir].state != pollMissing
			}
		}
		stale = stale || len(launched.Checks) == 0
		att = launched
	}
	if !stale {
		return &att, nil
	}
	// Stop every recorded unit before the folder goes: a sibling that still runs
	// must not lose its workspace and its cache protection underneath it. The
	// run's keeper removes the folder when its own lifetime ends; the removal here
	// only speeds that up.
	var script string
	for _, w := range batch {
		script += "bash -c " + shellquote.Quote(detachedStopScript) + " dyna-ci " + shellquote.Quote(w.spec.Dir) + " " + shellquote.Quote(w.spec.Unit) + " || true; "
	}
	var out tailBuffer
	_ = r.nodeCommand(ctx, &out, &out, script+removeRunDir(base, path.Join(runDir, ".active")))
	if err := store.deleteAttachment(ctx, repo, sha, r.Node.Name); err != nil {
		log.Printf("local CI %s: forget started checks: %v", r.Node.Name, err)
	}
	return nil, nil
}
