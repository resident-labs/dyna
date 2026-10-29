package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/protectedpath"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
)

// sweepArchivedRuns uses the same removal path as dyna runs remove.
// Goals and PR records currently store session IDs, not source workflow run IDs;
// there is no persisted source-run reference to guard here.
func sweepArchivedRuns(ctx context.Context, now time.Time, retention time.Duration, logf func(string, ...any)) (Pruned, error) {
	if retention <= 0 {
		return Pruned{}, nil
	}
	runs, err := runstore.ListArchived()
	if err != nil {
		return Pruned{}, err
	}
	kept := errors.New("keep archived run")
	var freed Pruned
	var errs []error
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return freed, errors.Join(append(errs, err)...)
		}
		if !runstore.IsArchived(run.ID) || !runstore.IsFinishedStatus(run.Status) {
			continue
		}
		var size int64
		var age time.Duration
		err := runstore.RemoveWith(run.ID, func(meta runstore.Meta) error {
			if !runstore.IsArchived(run.ID) || !runstore.IsFinishedStatus(meta.Status) {
				return kept
			}
			dir := filepath.Join(runstore.ArchiveDir(), run.ID)
			info, err := os.Stat(dir)
			if err != nil {
				return err
			}
			// A frozen recovery record stays protected even after its retention cutoff.
			if info.Mode().Perm()&0o222 == 0 {
				protectedpath.LogOnce(dir, logf, "protected read-only archived run %s: skipping retention", run.ID)
				return kept
			}
			// Archiving can change directory mtimes, and files can change after completion.
			// Keep the newest mtime (including directories) or EndedAt, whichever is later,
			// so a recent edit or completion always starts a fresh retention period.
			var newest time.Time
			size, newest, err = treeSize(ctx, dir)
			changed := meta.EndedAt
			if newest.After(changed) {
				changed = newest
			}
			if err != nil {
				return err
			}
			age = now.Sub(changed)
			if age <= retention {
				return kept
			}
			return ctx.Err()
		})
		if errors.Is(err, kept) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove archived run %s: %w", run.ID, err))
			continue
		}
		freed.Items++
		freed.Bytes += size
		if logf != nil {
			logf("removed archived run %s: age %s, freed %d bytes", run.ID, age, size)
		}
	}
	return freed, errors.Join(errs...)
}

// Retention ages. Each pruning kind has one constant here. Changing one
// makes the next pass a dry pass for that kind (see dryPassDue).
const (
	// sessionLogRetention keeps a closed session's raw stdout. The normalized
	// output stays the transcript source.
	sessionLogRetention = 24 * time.Hour
	// reviewInputRetention keeps a PR's reviewer input packs after it closes
	// or merges.
	reviewInputRetention = 7 * 24 * time.Hour
	// rejectedSpoolRetention keeps the runner spool lines the engine
	// rejected, for a bug report.
	rejectedSpoolRetention = 7 * 24 * time.Hour
	// closedSessionRetention keeps a closed session's folder and record.
	closedSessionRetention = 30 * 24 * time.Hour
	// shellJobRetention keeps the row of an ended shell job. The table grew by
	// about 26,000 rows a day and nothing removed them.
	shellJobRetention = 3 * 24 * time.Hour
	// localCIResultRetention keeps a local CI result after it was last saved,
	// unless its commit is an open PR's head or part of a pending merge trial.
	localCIResultRetention = 14 * 24 * time.Hour
)

// sweepClosedSessionLogs shares the scheduled maintenance pass with archived runs.
// The normalized output remains the transcript source; only raw stdout expires.
func sweepClosedSessionLogs(ctx context.Context, now time.Time, dry bool, logf func(string, ...any)) (Pruned, error) {
	ids, err := sessionlife.SafeIDs(runstore.SessionsDir())
	if err != nil {
		return Pruned{}, err
	}
	var freed Pruned
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return freed, errors.Join(append(errs, err)...)
		}
		bytes, err := removeClosedSessionLogs(ctx, id, now, dry)
		if bytes > 0 {
			freed.Items++
			freed.Bytes += bytes
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove session logs %s: %w", id, err))
		}
		if bytes > 0 && logf != nil && !dry {
			logf("removed closed session logs %s: freed %d bytes", id, bytes)
		}
	}
	return freed, errors.Join(errs...)
}

// sweepClosedSessions removes the folder and record of each session closed
// longer than closedSessionRetention, unless retain reports that it still
// matters (open children, goals, PR roles, kept worktrees). Dry only measures.
func sweepClosedSessions(ctx context.Context, now time.Time, dry bool, retainer ClosedSessionRetainer, logf func(string, ...any)) (Pruned, error) {
	sessions := runstore.SessionsDir()
	old := func(raw []byte) bool {
		var meta sessionlife.Snapshot
		return json.Unmarshal(raw, &meta) == nil && meta.IsClosed() && now.Sub(meta.ClosedAt()) > closedSessionRetention
	}
	var ids []string
	err := sessionlife.Each(sessions, false, func(id string, raw []byte, err error) error {
		if err == nil && old(raw) {
			ids = append(ids, id)
		}
		return ctx.Err()
	})
	if err != nil && !errors.Is(err, sessionlife.ErrRecord) {
		return Pruned{}, err
	}
	if len(ids) == 0 {
		return Pruned{}, nil
	}
	retain, err := retainer(ctx)
	if err != nil {
		return Pruned{}, err
	}
	var freed Pruned
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return freed, errors.Join(append(errs, err)...)
		}
		bytes, removed, err := removeClosedSession(ctx, sessions, id, old, retain, dry)
		if removed {
			freed.Items++
			freed.Bytes += bytes
			if logf != nil && !dry {
				logf("removed closed session %s: freed %d bytes", id, bytes)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove closed session %s: %w", id, err))
		}
	}
	return freed, errors.Join(errs...)
}

// removeClosedSession asks retain first, before the session's locks: retain takes
// other stores' locks (PR state, shell and run publication), which the
// dormant close also takes before the session lock. Work cannot be added to a
// closed session, so under the session's locks only the record is checked
// again. It then removes the folder and, last, the record, so a stop between
// the two leaves a record that the next pass removes.
func removeClosedSession(ctx context.Context, sessions, id string, old func([]byte) bool, retain func(context.Context, string) (bool, error), dry bool) (int64, bool, error) {
	if retained, err := retain(ctx, id); err != nil || retained {
		return 0, false, err
	}
	dir := filepath.Join(sessions, id)
	// Reopening and turn allocation hold session.lock; a runner holds
	// turn.lock through its final writes. Never wait on either.
	if _, err := os.Stat(dir); err == nil {
		unlock, owned, err := store.TryLock(filepath.Join(dir, "session.lock"))
		if err != nil || !owned {
			return 0, false, err
		}
		defer unlock()
		releaseTurn, owned, err := store.TryLock(filepath.Join(dir, "turn.lock"))
		if err != nil || !owned {
			return 0, false, err
		}
		defer releaseTurn()
	}
	raw, _, err := sessionlife.LoadRaw(sessions, id)
	if err != nil || !old(raw) {
		return 0, false, err
	}
	size, _, err := treeSize(ctx, dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, false, err
	}
	if dry {
		return size, true, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, false, err
	}
	return size, true, sessionlife.Delete(sessions, id)
}

// removeClosedSessionLogs only measures what it would free when dry is set.
func removeClosedSessionLogs(ctx context.Context, id string, now time.Time, dry bool) (int64, error) {
	dir := filepath.Join(runstore.SessionsDir(), id)
	r, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	var meta sessionlife.Snapshot
	eligible := func() (bool, error) {
		var err error
		if meta, err = sessionlife.Read(runstore.SessionsDir(), id); err != nil {
			return false, err
		}
		return meta.ID == id && meta.IsClosed() && now.Sub(meta.ClosedAt()) > sessionLogRetention, nil
	}
	ok, err := eligible()
	if err != nil || !ok {
		return 0, err
	}
	// Reopening and turn allocation both hold session.lock. Never wait on a
	// busy lifecycle operation; leave its logs for the next maintenance pass.
	unlock, owned, err := store.TryLock(filepath.Join(dir, "session.lock"))
	if err != nil || !owned {
		return 0, err
	}
	defer unlock()
	// A runner owns turn.lock through its final writes. A closed timestamp
	// alone does not prove that this writer has finished.
	releaseTurn, owned, err := store.TryLock(filepath.Join(dir, "turn.lock"))
	if err != nil || !owned {
		return 0, err
	}
	defer releaseTurn()
	meta = sessionlife.Snapshot{}
	ok, err = eligible()
	if err != nil || !ok {
		return 0, err
	}
	if meta.Turns > 0 {
		status, ok, err := sessionlife.TurnStatus(runstore.SessionsDir(), id, meta.Turns)
		if err != nil {
			return 0, err
		}
		if !ok || !sessionlife.TerminalTurn(status) {
			return 0, nil
		}
	}
	var freed int64
	for _, name := range []string{"agents/1/stdout.log"} {
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		info, err := r.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return freed, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if dry {
			freed += info.Size()
			continue
		}
		if err := r.Remove(name); err != nil {
			return freed, err
		}
		freed += info.Size()
	}
	return freed, nil
}

// treeSize returns the bytes of regular files under dir and its newest change,
// directories included.
func treeSize(ctx context.Context, dir string) (int64, time.Time, error) {
	var size int64
	var newest time.Time
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, newest, err
}

// sweepRejectedSpools removes the rejected runner spool files in dir that
// have not changed for rejectedSpoolRetention. Dry only measures.
func sweepRejectedSpools(ctx context.Context, dir string, now time.Time, dry bool) (Pruned, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Pruned{}, nil
	}
	if err != nil {
		return Pruned{}, err
	}
	var freed Pruned
	var errs []error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return freed, errors.Join(append(errs, err)...)
		}
		info, err := e.Info()
		if err != nil || !strings.HasSuffix(e.Name(), ".rejected.jsonl") || now.Sub(info.ModTime()) <= rejectedSpoolRetention {
			continue
		}
		freed.Items++
		freed.Bytes += info.Size()
		if !dry {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return freed, errors.Join(errs...)
}

// shellJobBatchesPerPass bounds one pass at 10,000 rows. The next pass resumes
// with the oldest remaining rows, so a large backlog cannot take the whole
// maintenance deadline.
const shellJobBatchesPerPass = 20

// pruneShellJobs deletes ended shell job rows older than shellJobRetention in
// bounded batches, one short write each. A later pass resumes the remainder.
func pruneShellJobs(ctx context.Context, shs *shells.Store, now time.Time) (int, error) {
	total := 0
	for range shellJobBatchesPerPass {
		n, err := shs.PruneEnded(ctx, now.Add(-shellJobRetention), shells.PruneBatch)
		total += n
		if err != nil || n < shells.PruneBatch || ctx.Err() != nil {
			return total, err
		}
	}
	return total, nil
}

// liveCommits lists the commits whose local CI result must stay: the heads and
// review heads of open PRs, and the changes of merge trials still in progress.
func liveCommits(st state.State) map[string]bool {
	keep := map[string]bool{}
	for _, r := range st.Records {
		if r == nil || r.State != state.PROpen {
			continue
		}
		for _, sha := range []string{r.Head, r.RoundHead, r.LastReviewHead, r.CIBudgetHead, r.CITestHead} {
			keep[sha] = true
		}
	}
	for _, trial := range st.MergeTrials {
		if trial == nil || trial.Phase == state.MergeTrialPhasePublished {
			continue
		}
		// The tested merge commit is trial.Head and each stack's Head; resuming a
		// trial reads its saved result by those, not by the source changes.
		keep[trial.Head], keep[trial.OriginalHead] = true, true
		for _, stack := range trial.Stacks {
			keep[stack.Head] = true
		}
		for _, change := range append(slices.Clone(trial.Changes), trial.StackChanges...) {
			keep[change.Head] = true
		}
	}
	return keep
}

// pruneLocalCIResults deletes local CI results older than localCIResultRetention
// whose commit is not live, and kept run records past their attachment TTL.
func pruneLocalCIResults(ctx context.Context, dataDir string, now time.Time) (int, error) {
	st, err := state.Read(state.Root(dataDir))
	if err != nil {
		return 0, err
	}
	store := localci.Store{DataDir: dataDir}
	results, err := store.PruneResults(ctx, now.Add(-localCIResultRetention), liveCommits(st))
	if err != nil {
		return 0, err
	}
	attachments, err := store.PruneAttachments(ctx, now)
	return results + attachments, err
}
