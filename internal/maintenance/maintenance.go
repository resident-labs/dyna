// Package maintenance runs host disk maintenance independently of PR watching.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

const runTimeout = 2 * time.Minute

// pass holds one maintenance pass at a time. The hourly built-in hook and the
// loop's disk-pressure pass both call Run, and two passes must not delete the
// same trees at once. A pass that waits for the lock stops if its context ends.
var pass = make(chan struct{}, 1)

// Result describes completed removals and the reasons other trees were kept.
type Result struct {
	BytesFreed int64
	Trees      []worktrees.Tree
	Kept       map[string]int
	// Pruned totals each kind of removal, so every pass reports what it freed.
	Pruned map[string]Pruned
}

// Options restricts a lifecycle pass or selects disk-pressure scratch cleanup.
type Options struct {
	Logf        func(string, ...any)
	Now         time.Time // Zero uses the host clock.
	Session     string
	Repos       []string
	OwnerClosed func(string) (bool, error)
	Pressure    bool
	// RetainClosedSession returns, once per pass, a check that reports
	// whether a long-closed session still matters (open children, goals, PR
	// roles, kept worktrees, sessions that still read it). The session
	// manager sets it; without it closed sessions are not removed.
	RetainClosedSession ClosedSessionRetainer
}

// CleanBranch is the merge cleanup: it removes a merged branch's worktrees in
// repos, and nothing else. The folder-wide sweeps belong to the scheduled
// pass (Run, from Loop: at start, each hour, and under disk pressure), not to
// every merge.
func CleanBranch(ctx context.Context, repos []string, branch string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	result := Result{Kept: map[string]int{}, Pruned: map[string]Pruned{}}
	err := cleanWorktrees(ctx, &result, repos, worktrees.GCOptions{Branch: branch})
	return result, err
}

// Run collects worktrees before cleaning scratch files. The total pass is bounded.
// One pass runs at a time, so the schedule and a by-hand run never overlap.
func Run(ctx context.Context, options ...Options) (Result, error) {
	select {
	case pass <- struct{}{}:
		defer func() { <-pass }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	result := Result{Kept: map[string]int{}, Pruned: map[string]Pruned{}}
	repos := opts.Repos
	var discoveryErr error
	if repos == nil {
		repos, discoveryErr = discover(ctx, opts.Session)
	}
	errs := []error{discoveryErr, cleanWorktrees(ctx, &result, repos, worktrees.GCOptions{Session: opts.Session, OwnerClosed: opts.OwnerClosed})}
	cleaned, scratchErr := scratch.Sweep(ctx, scratch.Options{Only: opts.Session, Lookup: scratchOwner, Logf: opts.Logf})
	result.add("scratch", Pruned{Items: len(cleaned.Removed), Bytes: cleaned.BytesFreed})
	for _, kept := range cleaned.Kept {
		result.Kept["scratch: "+kept.Reason]++
	}
	errs = append(errs, scratchErr)
	// Session cleanup must not remove unrelated archived workflow runs.
	if opts.Session == "" {
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		v, err := settings.Load("")
		if err == nil {
			freed, sweepErr := sweepArchivedRuns(ctx, now, v.RunsRetention, opts.Logf)
			result.add("archived runs", freed)
			err = sweepErr
		}
		errs = append(errs, err)
		errs = append(errs, sweepWithDryPass(ctx, &result, "closed session stdout", sessionLogRetention, func(dry bool) (Pruned, error) {
			return sweepClosedSessionLogs(ctx, now, dry, opts.Logf)
		}))
		if opts.RetainClosedSession != nil {
			errs = append(errs, sweepWithDryPass(ctx, &result, "closed sessions", closedSessionRetention, func(dry bool) (Pruned, error) {
				return sweepClosedSessions(ctx, now, dry, opts.RetainClosedSession, opts.Logf)
			}))
		}
		errs = append(errs, sweepWithDryPass(ctx, &result, "rejected runner spool lines", rejectedSpoolRetention, func(dry bool) (Pruned, error) {
			return sweepRejectedSpools(ctx, sessionlife.RunnerSpoolDir(store.DataDir()), now, dry)
		}))
		errs = append(errs, sweepWithDryPass(ctx, &result, "review inputs", reviewInputRetention, func(dry bool) (Pruned, error) {
			return sweepReviewInputs(ctx, state.Root(store.DataDir()), now, dry)
		}))
		errs = append(errs, sweepWithDryPass(ctx, &result, "stale test folders", testFolderRetention, func(dry bool) (Pruned, error) {
			return sweepTestFolders(now, os.TempDir(), processAlive, dry)
		}))
		// PR state removes closed records 14 days after close on each write;
		// the pass reports how many it removed since the last pass.
		if n, err := state.TakePruned(state.Root(store.DataDir())); err != nil {
			errs = append(errs, err)
		} else {
			result.add("closed PR records", Pruned{Items: n})
		}
		if n, err := state.PruneDoneNotices(ctx, state.Root(store.DataDir()), now); err != nil {
			errs = append(errs, fmt.Errorf("delivered PR notices: %w", err))
		} else {
			result.add("delivered PR notices", Pruned{Items: n})
		}
		if n, err := (cihealth.Store{DataDir: store.DataDir()}).Prune(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("CI health rows: %w", err))
		} else {
			result.add("CI health rows", Pruned{Items: n})
		}
	}
	return result, errors.Join(errs...)
}

// cleanWorktrees runs the worktree cleanup for each repository and adds the
// trees to result.
func cleanWorktrees(ctx context.Context, result *Result, repos []string, gc worktrees.GCOptions) error {
	var errs []error
	seen := map[string]bool{}
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		trees, err := worktrees.GC(ctx, repo, gc)
		if err != nil {
			errs = append(errs, fmt.Errorf("worktree cleanup %s: %w", repo, err))
		}
		for _, tree := range trees {
			if seen[tree.Path] {
				continue
			}
			seen[tree.Path] = true
			result.Trees = append(result.Trees, tree)
			if tree.Action == "removed" {
				result.add("worktrees", Pruned{Items: 1, Bytes: tree.Size})
			} else {
				reason, _, _ := strings.Cut(tree.Reason, "; owner inferred from")
				result.Kept[reason]++
			}
		}
	}
	return errors.Join(errs...)
}

// discover includes session working directories so handmade worktrees are seen.
func discover(ctx context.Context, session string) ([]string, error) {
	records, err := worktrees.List()
	candidates := map[string]bool{}
	for _, rec := range records {
		if rec.Repo != "" && (session == "" || rec.Session == session) {
			candidates[rec.Repo] = true
		}
	}
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	listErr := sessionlife.Each(runstore.SessionsDir(), false, func(id string, raw []byte, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var rec struct {
			Dir string `json:"dir"`
		}
		if err == nil && (session == "" || id == session) && json.Unmarshal(raw, &rec) == nil && rec.Dir != "" {
			candidates[rec.Dir] = true
		}
		return nil
	})
	if listErr != nil && !errors.Is(listErr, sessionlife.ErrRecord) && !errors.Is(listErr, ctx.Err()) {
		errs = append(errs, listErr)
	}
	// Deduplicate by the common Git directory, including sessions in linked trees.
	common := map[string]string{}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(probe, "/usr/bin/git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
		out, e := childenv.Clean(cmd).Output()
		cancel()
		if e != nil {
			continue
		}
		key := strings.TrimSpace(string(out))
		if resolved, e := filepath.EvalSymlinks(key); e == nil {
			key = resolved
		}
		if _, exists := common[key]; !exists {
			common[key] = path
		}
	}
	repos := make([]string, 0, len(common))
	for _, path := range common {
		repos = append(repos, path)
	}
	sort.Strings(repos)
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return repos, errors.Join(errs...)
}

// space is the free and total space of one file system, and whether it lives
// in memory. Many hosts hold /tmp in a tmpfs, which is a share of RAM.
type space struct {
	Free, Total uint64
	InMemory    bool
}

// freeSpace measures the file system that holds path, using the closest
// existing ancestor when the path is new.
func freeSpace(path string) (space, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(path, &st)
		if err == nil {
			return space{Free: st.Bavail * uint64(st.Bsize), Total: st.Blocks * uint64(st.Bsize), InMemory: inMemory(&st)}, nil
		}
		parent := filepath.Dir(path)
		if !os.IsNotExist(err) || parent == path {
			return space{}, err
		}
		path = parent
	}
}

// inMemoryFreeShare is the share of an in-memory file system that must stay
// free. The owner's minimum free space is an absolute size, which a tmpfs of a
// few GB can never reach, so a tmpfs is judged against its own size instead.
const inMemoryFreeShare = 15

// lowSpace reports whether a file system is under pressure: below the owner's
// minimum free space on a disk, or below a share of the whole size on an
// in-memory file system.
func lowSpace(s space, minFree uint64) bool {
	if s.InMemory && s.Total > 0 {
		return s.Free*100 < s.Total*inMemoryFreeShare
	}
	return s.Free < minFree
}

// lowDisk is one location measured below the free-space floor, with the free
// space it has left.
type lowDisk struct {
	Path      string
	FreeBytes int64
}

type scheduler struct {
	lastRun  time.Time
	pressure bool
	// low holds the locations of the last check that ran, which a later check
	// that cannot run keeps.
	low []lowDisk
}

func (s *scheduler) tick(ctx context.Context, now time.Time, low []lowDisk, run func(context.Context) (Result, error), alert func(notices.Notice) error) error {
	pressure := len(low) > 0
	firstPressure := pressure && !s.pressure
	s.pressure = pressure
	s.low = low
	// The routine hourly pass is the built-in maintenance hook's job, so this
	// loop runs only while disk pressure holds, and then at most once an hour.
	if !pressure {
		return nil
	}
	if !firstPressure && !s.lastRun.IsZero() && now.Sub(s.lastRun) < time.Hour {
		return nil
	}
	s.lastRun = now
	result, err := run(ctx)
	if firstPressure {
		paths := make([]notices.DiskPath, len(low))
		for i, l := range low {
			paths[i] = notices.DiskPath{Path: l.Path, FreeBytes: l.FreeBytes}
		}
		reasons := make([]string, 0, len(result.Kept))
		for reason, count := range result.Kept {
			reasons = append(reasons, fmt.Sprintf("%s: %d", reason, count))
		}
		sort.Strings(reasons)
		err = errors.Join(err, alert(notices.Notice{
			Key:      "disk",
			Kind:     notices.KindDisk,
			Severity: notices.SeverityError,
			Disk: &notices.DiskFacts{
				Paths:             paths,
				FreedBytes:        result.BytesFreed,
				Kept:              reasons,
				MaintenanceFailed: err != nil,
			},
			Command: "dyna maintenance run",
		}))
	}
	return err
}

// ClosedSessionRetainer builds, once per pass, the check that reports
// whether a long-closed session must be retained.
type ClosedSessionRetainer func(context.Context) (retain func(context.Context, string) (bool, error), err error)

// Pass is the scheduled maintenance entry point: one pass of Run with the
// defaults the engine uses for a timer tick, recorded so
// `dyna maintenance status` lists it. The built-in maintenance hook calls it.
// Disk-pressure cleanup stays in the engine's own loop.
func Pass(ctx context.Context, retain ClosedSessionRetainer, logf func(string, ...any)) (Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	start := time.Now()
	result, err := Run(ctx, Options{Logf: logf, RetainClosedSession: retain})
	logf("maintenance: %s", result.Summary())
	if saveErr := SaveReport(store.DataDir(), start, time.Since(start), result, err); saveErr != nil {
		logf("maintenance report: %v", saveErr)
	}
	return result, err
}

// Loop checks disk pressure each minute and runs a cleanup pass while space is
// low, at most once an hour. It executes passes serially and stops when the
// host context is canceled. The routine hourly pass and the hook schedule are
// the engine workers' job: the built-in maintenance hook and the Hook schedule
// worker, which run only under the worker lock.
// retainer is passed to each pass as Options.RetainClosedSession.
func Loop(ctx context.Context, logf func(string, ...any), retainer ClosedSessionRetainer) {
	loop(ctx, logf, retainer)
}

func loop(ctx context.Context, logf func(string, ...any), retainer ClosedSessionRetainer) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if ctx.Err() != nil {
		return
	}
	defer func() {
		if ctx.Err() != nil {
			logf("maintenance: stopped for engine shutdown")
		}
	}()
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	var schedule scheduler
	for {
		if ctx.Err() != nil {
			return
		}
		v, err := settings.Load("")
		var low []lowDisk
		if err == nil && v.WorktreesMinFreeBytes > 0 {
			root, rootErr := worktrees.ManagedRoot()
			err = rootErr
			paths := []string{"/tmp"}
			if rootErr == nil {
				paths = append(paths, root)
			}
			for _, path := range paths {
				measured, e := freeSpace(path)
				if e != nil {
					err = errors.Join(err, fmt.Errorf("free space %s: %w", path, e))
					continue
				}
				if lowSpace(measured, uint64(v.WorktreesMinFreeBytes)) {
					low = append(low, lowDisk{Path: path, FreeBytes: int64(measured.Free)})
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logf("maintenance pressure check: %v", err)
			if len(low) == 0 {
				low = schedule.low
			}
		}
		maintenanceErr := schedule.tick(ctx, time.Now(), low, func(ctx context.Context) (Result, error) {
			start := time.Now()
			result, err := Run(ctx, Options{Pressure: len(low) > 0, Logf: logf, RetainClosedSession: retainer})
			logf("maintenance: %s", result.Summary())
			if saveErr := SaveReport(store.DataDir(), start, time.Since(start), result, err); saveErr != nil {
				logf("maintenance report: %v", saveErr)
			}
			return result, err
		}, ownerAlert)
		if ctx.Err() != nil {
			return
		}
		if maintenanceErr != nil {
			logf("maintenance: %v", maintenanceErr)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func ownerAlert(n notices.Notice) error {
	_, err := notices.DefaultStore().Post(n)
	return err
}

// scratchOwner reads lifecycle metadata without loading or recovering sessions.
func scratchOwner(owner string) scratch.Owner {
	return scratchOwnerWith(owner, ownerReaders{
		session: func(id string) (time.Time, error) {
			meta, err := sessionlife.Read(runstore.SessionsDir(), id)
			if errors.Is(err, sessionlife.ErrRecord) {
				// A corrupt record proves nothing about the owner: treat it as unknown.
				err = os.ErrNotExist
			}
			return meta.ClosedAt(), err
		},
		run: func(id string) (time.Time, error) {
			if runstore.ValidateRunID(id) != nil {
				// A folder that is not a run or session name has no owner record.
				return time.Time{}, os.ErrNotExist
			}
			meta, err := runstore.ReadMeta(id)
			ended := meta.EndedAt
			if meta.Status == "running" {
				ended = time.Time{}
			}
			return ended, err
		},
		liveShell: func(id string) (bool, error) {
			list, err := shells.DefaultStore().List(false)
			return liveShellFor(list, id), err
		},
	})
}

// liveShellFor reports whether a running or starting job belongs to id. A
// workflow worker's job names its session in Owner and the run in Run, so a run
// ID matches on Run.
func liveShellFor(list []shells.Info, id string) bool {
	for _, in := range list {
		if (in.Owner == id || in.Run == id) && (in.Status == "running" || in.Status == "starting") {
			return true
		}
	}
	return false
}

// ownerReaders are the state reads behind scratchOwner. Each close-time reader
// returns a zero time for an owner that has not closed.
type ownerReaders struct {
	session   func(id string) (time.Time, error)
	run       func(id string) (time.Time, error)
	liveShell func(id string) (bool, error)
}

// scratchOwnerWith keeps a folder when it cannot prove that the owner is gone:
// a read that fails for any reason other than a missing record, or a shell job
// that still runs, counts as open. Only a missing record makes an owner unknown.
func scratchOwnerWith(owner string, read ownerReaders) scratch.Owner {
	parts := strings.Split(owner, "/")
	if len(parts) > 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." || strings.ContainsAny(parts[0], "\\") {
		return scratch.Owner{}
	}
	closed := read.run
	if runstore.IsSessionDirID(parts[0]) {
		closed = read.session
	}
	closedAt, err := closed(parts[0])
	if errors.Is(err, os.ErrNotExist) {
		return scratch.Owner{}
	}
	if err != nil {
		return scratch.Owner{Known: true, Open: true}
	}
	if live, err := read.liveShell(parts[0]); live || err != nil {
		return scratch.Owner{Known: true, Open: true}
	}
	return scratch.Owner{Known: true, Open: closedAt.IsZero(), ClosedAt: closedAt}
}
