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
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

const runTimeout = 2 * time.Minute

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
	// ClosedSessionKeeper returns, once per pass, a check that reports
	// whether a long-closed session still matters (open children, goals, PR
	// roles, kept worktrees, sessions that still read it). The session
	// manager sets it; without it closed sessions are not removed.
	ClosedSessionKeeper Keeper
}

// RunSession runs the same maintenance pass for a single session.
func RunSession(ctx context.Context, id string) (Result, error) {
	if id == "" {
		return Result{}, errors.New("maintenance session ID is required")
	}
	return Run(ctx, Options{Session: id})
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
func Run(ctx context.Context, options ...Options) (Result, error) {
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
	cleaned, scratchErr := scratch.Sweep(ctx, scratch.Options{Only: opts.Session, Pressure: opts.Pressure, Lookup: scratchOwner, Logf: opts.Logf})
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
		if opts.ClosedSessionKeeper != nil {
			errs = append(errs, sweepWithDryPass(ctx, &result, "closed sessions", closedSessionRetention, func(dry bool) (Pruned, error) {
				return sweepClosedSessions(ctx, now, dry, opts.ClosedSessionKeeper, opts.Logf)
			}))
		}
		errs = append(errs, sweepWithDryPass(ctx, &result, "rejected runner spool lines", rejectedSpoolRetention, func(dry bool) (Pruned, error) {
			return sweepRejectedSpools(ctx, sessionlife.RunnerSpoolDir(store.DataDir()), now, dry)
		}))
		errs = append(errs, sweepWithDryPass(ctx, &result, "review inputs", reviewInputRetention, func(dry bool) (Pruned, error) {
			return sweepReviewInputs(ctx, state.Root(store.DataDir()), now, dry)
		}))
		// PR state removes closed records 14 days after close on each write;
		// the pass reports how many it removed since the last pass.
		if n, err := state.TakePruned(state.Root(store.DataDir())); err != nil {
			errs = append(errs, err)
		} else {
			result.add("closed PR records", Pruned{Items: n})
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

// freeBytes uses the closest existing ancestor when the managed root is new.
func freeBytes(path string) (uint64, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(path, &st)
		if err == nil {
			return st.Bavail * uint64(st.Bsize), nil
		}
		parent := filepath.Dir(path)
		if !os.IsNotExist(err) || parent == path {
			return 0, err
		}
		path = parent
	}
}

type scheduler struct {
	lastRun  time.Time
	pressure bool
}

func (s *scheduler) tick(ctx context.Context, now time.Time, pressure bool, run func(context.Context) (Result, error), alert func(string) error) error {
	firstPressure := pressure && !s.pressure
	s.pressure = pressure
	if !firstPressure && !s.lastRun.IsZero() && now.Sub(s.lastRun) < time.Hour {
		return nil
	}
	s.lastRun = now
	result, err := run(ctx)
	if firstPressure {
		text := fmt.Sprintf("Disk space is low. Maintenance freed %d bytes.", result.BytesFreed)
		reasons := make([]string, 0, len(result.Kept))
		for reason, count := range result.Kept {
			reasons = append(reasons, fmt.Sprintf("%s: %d", reason, count))
		}
		sort.Strings(reasons)
		if len(reasons) > 0 {
			text += " Items kept: " + strings.Join(reasons, "; ") + "."
		}
		if err != nil {
			text += " Some maintenance failed; see the host log."
		}
		err = errors.Join(err, alert(text))
	}
	return err
}

// Loop checks pressure each minute and runs cleanup at least once per hour.
// It executes passes serially and stops when the host context is canceled.
// Keeper builds, once per pass, the check that keeps a long-closed session.
type Keeper func(context.Context) (keep func(context.Context, string) (bool, error), err error)

// Loop runs scheduled maintenance until ctx ends. keep is passed to each pass
// as Options.ClosedSessionKeeper.
func Loop(ctx context.Context, logf func(string, ...any), keep Keeper) {
	loop(ctx, logf, hooks.New(), keep)
}

type lifecycleHooks interface {
	ReconcileInstall(context.Context) error
	Repositories(context.Context) ([]string, error)
	Tick(context.Context, string, time.Time) error
}

func loop(ctx context.Context, logf func(string, ...any), hookService lifecycleHooks, keep Keeper) {
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
		installErr := hookService.ReconcileInstall(ctx)
		if ctx.Err() != nil {
			return
		}
		if installErr != nil {
			logf("install hook results: %v", installErr)
		}
		// Lifecycle schedules run each minute, independent of cleanup cadence.
		// Hook storage records the durable main checkouts for this pass.
		repos, discoverErr := hookService.Repositories(ctx)
		if ctx.Err() != nil {
			return
		}
		if discoverErr != nil {
			logf("hook discovery: %v", discoverErr)
		}
		for _, repo := range repos {
			hookErr := hookService.Tick(ctx, repo, time.Now())
			if ctx.Err() != nil {
				return
			}
			if hookErr != nil {
				logf("hooks: %v", hookErr)
			}
		}
		v, err := settings.Load("")
		pressure := false
		if err == nil && v.WorktreesMinFreeBytes > 0 {
			root, rootErr := worktrees.ManagedRoot()
			err = rootErr
			paths := []string{"/tmp"}
			if rootErr == nil {
				paths = append(paths, root)
			}
			for _, path := range paths {
				free, e := freeBytes(path)
				if e != nil {
					err = errors.Join(err, fmt.Errorf("free space %s: %w", path, e))
					continue
				}
				if free < uint64(v.WorktreesMinFreeBytes) {
					pressure = true
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logf("maintenance pressure check: %v", err)
			if !pressure {
				pressure = schedule.pressure
			}
		}
		maintenanceErr := schedule.tick(ctx, time.Now(), pressure, func(ctx context.Context) (Result, error) {
			start := time.Now()
			result, err := Run(ctx, Options{Pressure: pressure, Logf: logf, ClosedSessionKeeper: keep})
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

func ownerAlert(text string) error {
	_, err := notices.DefaultStore().Post(notices.Notice{Key: "disk", Kind: "disk", Severity: "error", Text: text})
	return err
}

// scratchOwner reads lifecycle metadata without loading or recovering sessions.
func scratchOwner(owner string) scratch.Owner {
	parts := strings.Split(owner, "/")
	if len(parts) > 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." || strings.ContainsAny(parts[0], "\\") {
		return scratch.Owner{}
	}
	if runstore.IsSessionDirID(parts[0]) {
		meta, err := sessionlife.Read(runstore.SessionsDir(), parts[0])
		if err != nil {
			return scratch.Owner{}
		}
		return scratch.Owner{Known: true, Open: !meta.IsClosed(), ClosedAt: meta.ClosedAt()}
	}
	meta, err := runstore.ReadMeta(parts[0])
	if err != nil {
		return scratch.Owner{}
	}
	ended := meta.EndedAt
	if meta.Status == "running" {
		ended = time.Time{}
	}
	return scratch.Owner{Known: true, Open: ended.IsZero(), ClosedAt: ended}
}
