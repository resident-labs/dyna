package hooks

import (
	"context"
	"errors"
	"fmt"
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

// LibrarianHooks wake the repository's librarian after a merge, after
// origin/main moves, and on a schedule that runs merges batched inside the
// librarian's interval. dyna library enable adds them; disable removes them.
func LibrarianHooks(exe, main string) []Hook {
	command := shellquote.Quote(exe) + " library wake --repo " + shellquote.Quote(main)
	hook := func(name, event string, every int) Hook {
		return Hook{Name: name, Label: "Librarian", Event: event, Command: command, Cwd: "main", Timeout: "2m", OnFail: "alert", EveryMinutes: every}
	}
	return []Hook{hook("librarian-merged", PRMerged, 0), hook("librarian-main", MainUpdated, 0), hook("librarian-batch", Schedule, 10)}
}
