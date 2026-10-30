package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updaterservice"
)

// SelfUpdateRecipe only builds configuration. The owner enables it with hooks add.
func SelfUpdateRecipe(ctx context.Context, repo, exe, name string) (Hook, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return Hook{}, err
	}
	module, err := os.ReadFile(filepath.Join(r.Main, "go.mod"))
	if err != nil || !strings.HasPrefix(string(module), "module dyna-agent\n") {
		return Hook{}, fmt.Errorf("the self-update recipe requires the Dyna source repository")
	}
	if _, err := os.Stat(filepath.Join(r.Main, "desktop", "build.sh")); err != nil {
		return Hook{}, fmt.Errorf("the self-update recipe requires desktop/build.sh: %w", err)
	}
	var dirs []string
	for _, tool := range []string{"go", "npm", "node"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			return Hook{}, fmt.Errorf("self-update requires %s on PATH: %w", tool, err)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return Hook{}, err
		}
		dirs = append(dirs, filepath.Dir(path))
	}
	toolPath := capturedToolPath(append(dirs, filepath.SplitList(os.Getenv("PATH"))...), store.DataDir())
	h := Hook{Name: name, Label: "Update Dyna", Event: MainUpdated, Command: shellquote.Quote(exe) + " git hooks _install --main " + shellquote.Quote(r.Main) + " --name " + shellquote.Quote(name) + " --tool-path " + shellquote.Quote(toolPath), Cwd: "main", Timeout: "1m", OnFail: "alert"}
	return h, Validate(h)
}

// capturedToolPath excludes temporary agent-computer tools from saved recipes.
func capturedToolPath(entries []string, dataDir string) string {
	computers := filepath.Join(dataDir, "computers")
	seen := map[string]bool{}
	var kept []string
	for _, entry := range entries {
		if !filepath.IsAbs(entry) {
			continue
		}
		clean := filepath.Clean(entry)
		info, err := os.Stat(clean)
		if err != nil || !info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(computers, clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !seen[clean] {
			kept = append(kept, clean)
			seen[clean] = true
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

func (s Service) installer() hookinstall.Service {
	if s.Install != nil {
		return *s.Install
	}
	i := hookinstall.New()
	if s.DataDir != "" {
		i.Jobs.Root = filepath.Join(s.DataDir, "hooks", "install")
	}
	return i
}
func (s Service) pendingInstall(ctx context.Context, ev Run) (Run, error) {
	i := s.installer()
	if _, err := i.Jobs.Read(ev.ID); os.IsNotExist(err) {
		return ev, nil
	} else if err != nil {
		return ev, err
	}
	err := i.Jobs.Update(ev.ID, func(j *hookinstall.Job) error {
		ev.Result = "pending"
		if j.Recorded {
			ev.Result = j.Result
			ev.FinishedAt = j.FinishedAt
		}
		var err error
		ev, err = s.saveRun(ctx, ev)
		return err
	})
	return ev, err
}

// ReconcileInstall restores results after dyna-engine restarts. The job lock
// serializes log append, result replacement and the recorded flag. No hook or
// room lock is held while querying systemd or acquiring this job lock.
func (s Service) ReconcileInstall(ctx context.Context) error {
	i := s.installer()
	jobs, err := i.Jobs.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, job := range jobs {
		if job.Recorded {
			if err := i.Jobs.Archive(job.ID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		records, err := s.runs(ctx, job.Repo, job.Hook)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var ev Run
		for _, r := range records {
			if r.ID == job.ID {
				ev = r
				break
			}
		}
		if ev.Result != "pending" {
			if ev.Result != "pass" && job.Status == "finished" {
				_ = i.Jobs.Update(job.ID, func(j *hookinstall.Job) error { j.Recorded = true; return nil })
				i.CleanupUnit(ctx, job)
				if err := i.Jobs.Archive(job.ID); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		// A zero finish time is the launcher's emergency pending record. Give
		// its runner time to close the log; a restart can leave this record behind.
		if ev.FinishedAt.IsZero() && time.Since(job.StartedAt) < 3*time.Minute {
			continue
		}
		job, err = i.InspectUnit(ctx, job)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if job.Status != "finished" {
			continue
		}
		err = i.Jobs.Update(job.ID, func(j *hookinstall.Job) error {
			if j.Recorded {
				return nil
			}
			if err := hookinstall.Log(*j); err != nil {
				return err
			}
			ev.Result = j.Result
			ev.FinishedAt = j.FinishedAt
			if _, err := s.saveRun(ctx, ev); err != nil {
				return err
			}
			hs, err := s.List(ctx, j.Config.Main)
			if err != nil {
				return err
			}
			if j.Hook == updaterservice.RunName {
				hs = append(hs, Hook{Name: updaterservice.RunName, Label: "Updates"})
			}
			for _, h := range hs {
				if h.Name != j.Hook {
					continue
				}
				r, err := repoid.Resolve(ctx, j.Config.Main)
				if err != nil {
					return err
				}
				event := Event{Event: MainUpdated, Repo: j.Config.Main, Room: j.Room, Session: j.Session, SHA: j.SHA}
				// The unit's result always ends the run the hook started, passing or
				// not. Warnings from the checkout stay on the same card, and the
				// run's log holds them too.
				state := room.HookPassed
				if j.Result != "pass" {
					state = room.HookError
				}
				detail := warningText(j.Warnings)
				if state == room.HookPassed {
					s.emit(event, hookEvent(event, h, ev, state, detail))
					s.clearNotices(r.ID, h)
				} else {
					s.alert(r, event, h, ev, state, installCause(j), detail)
				}
			}
			j.Recorded = true
			return nil
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		i.CleanupUnit(ctx, job)
		if err := i.Jobs.Archive(job.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// installCause names why an install unit ended a run, for the claim that may
// create the repo's room. Every warning cause counts, so two different
// checkouts do not collapse into one claim.
func installCause(j *hookinstall.Job) string {
	cause := "install unit " + j.Result
	for _, w := range j.Warnings {
		cause += " " + w.Cause
	}
	return cause
}

// warningText joins the checkout warnings of one install unit into the one
// sentence its run card shows.
func warningText(warnings []hookinstall.Warning) string {
	lines := make([]string, 0, len(warnings))
	for _, w := range warnings {
		lines = append(lines, fmt.Sprintf("checkout %s: %s", w.Cause, w.Text))
	}
	return strings.Join(lines, " ")
}

func (s Service) clearNotices(id string, h Hook) {
	_ = s.runtime(id, func(st *state) error {
		for key := range st.Notices {
			if strings.HasPrefix(key, noticePrefix(h)) {
				delete(st.Notices, key)
			}
		}
		return nil
	})
}

// SettleInterrupted finishes the hook runs that an earlier engine left running.
// The self-update hook restarts the engine in the middle of its own run, so the
// engine that posted the start never posts a result, and the card would stay
// running forever. Install receipts settle their own runs through
// ReconcileInstall. A run that started before this engine and has no receipt
// can never finish, so it ends failed with the reason and keeps its log.
// started is this engine's start time, so a run this engine began is left alone.
func (s Service) SettleInterrupted(ctx context.Context, started time.Time) error {
	errs := []error{s.ReconcileInstall(ctx)}
	runs, err := s.pendingRuns(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	i := s.installer()
	for _, run := range runs {
		// A run that started after this engine did, such as one a drained spool
		// event began, is still live.
		if !run.StartedAt.Before(started) {
			continue
		}
		// An install unit owns its run until it records a result, and
		// ReconcileInstall finishes it.
		if _, err := i.Jobs.Read(run.ID); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, s.settleLeftRun(ctx, run, started))
	}
	return errors.Join(errs...)
}

// settleLeftRun ends one run that can never finish, writes why into its log,
// and posts the typed result that ends its card. The run itself keeps the
// event, the checkout and the room, because the hook may be gone by now. A run
// stored before it kept those falls back to the hook and to its start post.
func (s Service) settleLeftRun(ctx context.Context, run Run, now time.Time) error {
	main := run.Main
	if main == "" {
		// A run stored before it carried its checkout: the document keeps the
		// repository's main checkout even after its last hook is removed.
		main, _ = s.documentRepo(ctx, run.Repo)
	}
	detail := "the engine stopped during this run, so its result was never recorded"
	if line := lastLogLine(run.LogPath); line != "" {
		detail += ": " + line
	}
	writeDetail := func() {
		if file, err := os.OpenFile(run.LogPath, os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			_, _ = fmt.Fprintln(file, detail)
			_ = file.Close()
		}
	}
	if run.Repo == globalKey {
		// A global built-in hook belongs to no repository and no room, so its
		// run ends in the hook store that the Hooks screen reads.
		writeDetail()
		run.Result, run.FinishedAt = "error", now
		_, err := s.saveRun(ctx, run)
		return err
	}
	if main == "" {
		// A repository run whose checkout is gone has no room to post in, so it
		// ends in the hook store too.
		writeDetail()
		run.Result, run.FinishedAt = "error", now
		_, err := s.saveRun(ctx, run)
		return err
	}
	// The run names the hook as "hook-<name>", and a hook still in place
	// decides how the card reads. A removed hook still ends its own run from
	// what the run recorded.
	ev := Event{Event: run.Event, Repo: main, Worktree: main, Room: run.Room}
	h := Hook{Name: strings.TrimPrefix(run.Name, "hook-"), Event: run.Event, Label: run.Label, Command: run.Command}
	if hooks, err := s.load(ctx, run.Repo); err == nil {
		for _, old := range hooks {
			if "hook-"+old.Name == run.Name {
				h, ev.Event = old, old.Event
				break
			}
		}
	}
	if ev.Event == "" || run.Main == "" {
		// A run stored before it kept its event, checkout or room: its start
		// post names the event and the room its card is in, which is the
		// session's own room for a session hook. Room wins over the event here,
		// because the start post is the only record of where the card sits.
		if rec, event := startPost(ev, run); event != "" {
			ev.Event, ev.Room = event, rec.ID
		}
	}
	if ev.Event == "" {
		// Nothing names the event, and a result without one is refused. Leave
		// the run pending so the next start can try again.
		return fmt.Errorf("hook run %s has no event to report", run.ID)
	}
	// The result posts before the run is saved, so a post that fails leaves the
	// run pending for the next start instead of a card that never ends.
	run.Result, run.FinishedAt = "error", now
	if err := s.postEvent(ev, hookEvent(ev, h, run, room.HookError, detail), false); err != nil {
		return err
	}
	// The reason joins the log only once the card is posted, so a failed post
	// leaves the log as the run left it.
	writeDetail()
	_, err := s.saveRun(ctx, run)
	return err
}

// startPost finds the start card of a run: the room it was posted in, and the
// event that card names. A run stored before it kept its own room needs both,
// because a session hook posts in the session's room. The session's address,
// which the run records, names that room.
func startPost(e Event, run Run) (room.Record, string) {
	rooms := room.DefaultStore()
	for _, rec := range startRooms(rooms, e, run) {
		msgs, err := rooms.Messages(rec.ID)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if h, ok := m.Hook(); ok && h.RunID == run.ID && h.State == room.HookRunning {
				return rec, h.Event
			}
		}
	}
	return room.Record{}, ""
}

// startRooms lists the rooms a run's start card can be in: the room its event
// named, the room of the session it ran for, and the repository's room.
func startRooms(rooms *room.Store, e Event, run Run) []room.Record {
	var out []room.Record
	seen := map[string]bool{}
	add := func(rec room.Record, ok bool) {
		if ok && !seen[rec.ID] {
			seen[rec.ID] = true
			out = append(out, rec)
		}
	}
	if e.Room != "" {
		if rec, err := rooms.Resolve(e.Room); err == nil {
			add(rec, true)
		}
	}
	// A session's address is its room name and its name, as in "dyna:root".
	if name, _, ok := strings.Cut(run.RunnerSession, ":"); ok && name != "" {
		if rec, err := rooms.Resolve(name); err == nil {
			add(rec, true)
		}
	}
	if root, err := room.RepoRoot(e.Repo); err == nil {
		rec, found, err := rooms.RepoRoom(root)
		add(rec, err == nil && found)
	}
	return out
}

// lastLogLine reads the end of a run's log for the line its settled card shows.
// The runner writes its own error line, such as an exit status, last.
func lastLogLine(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	const tail = 512
	if info.Size() > tail {
		if _, err := file.Seek(info.Size()-tail, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(io.LimitReader(file, tail))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if runes := []rune(last); len(runes) > 160 {
		last = string(runes[:160])
	}
	return last
}
