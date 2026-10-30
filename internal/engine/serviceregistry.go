package engine

import (
	"context"
	"path/filepath"
	"strings"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/pr/issuewatch"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/serviceregistry"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/updaterservice"
)

// serviceRegistry lists every service for a room. The CLI and the Services
// page both read this call, so they show the same rows. A caller that names a
// repository gets that repository's rows; otherwise the room's own repository
// applies. A part the engine
// cannot read leaves its service unreadable and does not hide the others.
func (s services) serviceRegistry(ctx context.Context, roomRef, repo string) ([]serviceregistry.Service, error) {
	rec, err := s.rooms.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	var f serviceregistry.Facts
	views, _ := s.rooms.ListServices(rec.ID)
	view := func(role string) (profile string, waiting bool) {
		for _, v := range views {
			if v.Role == role {
				return v.Profile, strings.HasPrefix(v.Reason, "waiting:")
			}
		}
		return "", false
	}
	if st, err := s.rooms.PRs.ReviewerStatus(rec.ID); err == nil {
		r := serviceregistry.ReviewerFacts{Enabled: st.Enabled, Failing: st.LastError != "", Queued: len(st.Queue)}
		r.Profile, r.ProfileWaiting = view("reviewer")
		for _, slot := range st.Reviewers {
			if slot.Current != nil {
				r.Reviewing = true
			}
			if r.Session == "" && slot.Session != nil {
				r.Session = slot.Session.Address
			}
		}
		if n := len(st.Recent); n > 0 {
			r.LastVerdictAt = st.Recent[n-1].At
		}
		f.Reviewer = &r
	}
	if st, err := s.rooms.PRs.MergeControlStatus(rec.ID); err == nil {
		m := serviceregistry.MergeFacts{Paused: st.Paused}
		for _, q := range st.Queue {
			switch q.State {
			case "trial-running":
				m.Running++
			case "ready":
				m.Ready++
			}
		}
		if history, err := s.rooms.PRs.RoomMergeHistory(rec.ID); err == nil {
			for _, h := range history {
				if h.Outcome == "published" && h.At.After(m.LastMerged) {
					m.LastMerged = h.At
				}
			}
		}
		f.Merge = &m
	}
	if st, err := s.sync.Status(); err == nil {
		for _, repo := range st {
			for _, remote := range repo.Remotes {
				f.LaneRemotes++
				f.LaneFailing = f.LaneFailing || remote.LastError != ""
				if remote.CheckedAt.After(f.LaneCheckedAt) {
					f.LaneCheckedAt = remote.CheckedAt
				}
			}
		}
	}
	if watches, err := (issuewatch.Store{DataDir: store.DataDir()}).Room(ctx, rec.ID); err == nil {
		f.IssueWatches = len(watches)
	}
	voice := s.voice.Status()
	f.Voice = &serviceregistry.VoiceFacts{Available: voice.Available, Live: voice.Live}
	f.Updates = updateFacts(ctx)

	f.Hooks = map[serviceregistry.ID]serviceregistry.HookFacts{}
	global := s.hookFacts(ctx, "")
	for id, h := range global {
		f.Hooks[serviceregistry.ID(id)] = h
	}
	if repo == "" {
		repo = rec.Repo
	}
	if repo != "" {
		f.Library = s.libraryFacts(ctx, repo, rec.Name, view)
	}
	return serviceregistry.Build(f), nil
}

func updateFacts(ctx context.Context) *serviceregistry.UpdateFacts {
	cfg, err := (updaterservice.Store{DataDir: store.DataDir()}).Get(ctx)
	if err != nil {
		return nil
	}
	u := &serviceregistry.UpdateFacts{Source: cfg.Source, Enabled: cfg.Enabled, EveryMinutes: cfg.EveryMinutes, MultipleHooks: cfg.MultipleHooks}
	if cfg.Source != "" {
		_, err := repoid.Resolve(ctx, cfg.Source)
		u.SourceInvalid = err != nil
	}
	jobs, err := hookinstall.DefaultStore().Recent(updaterservice.RunName)
	if err != nil {
		return nil
	}
	for _, job := range jobs {
		// hookinstall stores the same pass and error values as hooks.RunResult.
		result := hooks.RunResult(job.Result)
		if job.Status != "finished" {
			result = hooks.ResultPending
		}
		u.Runs = append(u.Runs, serviceregistry.HookRun{Result: result, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt})
	}
	return u
}

// hookFacts reads every hook of one scope (the machine when repo is empty)
// with all its runs, so the newest run decides a service's status.
func (s services) hookFacts(ctx context.Context, repo string) map[string]serviceregistry.HookFacts {
	var summaries []hooks.Summary
	var runs []hooks.Run
	var err error
	if repo == "" {
		summaries, err = s.hooks.SummariesBuiltin(ctx)
	} else {
		summaries, err = s.hooks.Summaries(ctx, repo)
	}
	if err != nil {
		return nil
	}
	runs, err = s.hooks.Runs(ctx, repo, "")
	if err != nil {
		return nil
	}
	out := map[string]serviceregistry.HookFacts{}
	for _, sum := range summaries {
		h := serviceregistry.HookFacts{Found: true, Disabled: sum.Hook.Disabled, NextRun: sum.NextRun}
		if sum.Hook.Builtin != "" {
			h.Schedule = &serviceregistry.Schedule{EveryMinutes: sum.Hook.EveryMinutes, Timeout: sum.Hook.Timeout}
		}
		for _, run := range runs {
			if run.Name == "hook-"+sum.Hook.Name {
				h.Runs = append(h.Runs, serviceregistry.HookRun{Result: run.Result, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt})
			}
		}
		out[sum.Hook.Name] = h
	}
	return out
}

func (s services) libraryFacts(ctx context.Context, repo, roomName string, view func(string) (string, bool)) *serviceregistry.LibraryFacts {
	cfg, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return nil
	}
	l := serviceregistry.LibraryFacts{Enabled: cfg.LibraryOn(filepath.Clean(repo)), Session: roomName + ":" + subagent.LibrarianName}
	l.Profile, l.ProfileWaiting = view("library")
	if r, err := repoid.Resolve(ctx, repo); err == nil {
		if st, err := (librarianstate.Store{DataDir: store.DataDir()}).Get(r.ID); err == nil {
			l.LastRun = st.LastRun
			l.Running = s.librarianService().Running(st)
		}
	}
	l.NextRun, _ = s.librarianDeferred(ctx, repo)
	return &l
}
