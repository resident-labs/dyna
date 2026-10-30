package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/room"
	"dyna-agent/internal/serviceregistry"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/updaterservice"
	"dyna-agent/internal/websvc"
)

func TestUpdateSettingAndControls(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	if _, err := s.setSetting(ctx, "updater.source", "/missing"); err != nil {
		t.Fatal(err)
	}
	off, interval := false, 17
	if err := editUpdate(ctx, hooks.ServicePatch{Disabled: &off, EveryMinutes: &interval}); err != nil {
		t.Fatal(err)
	}
	cfg, err := (updaterservice.Store{DataDir: store.DataDir()}).Get(ctx)
	if err != nil || cfg.Source != "/missing" || !cfg.Enabled || cfg.EveryMinutes != 17 {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updater := updaterservice.Store{DataDir: store.DataDir()}
	if _, due, err := updater.ClaimInterval(ctx, now); err != nil || !due {
		t.Fatalf("first claim: %v %v", due, err)
	}
	if _, due, err := updater.ClaimInterval(ctx, now.Add(time.Minute)); err != nil || due {
		t.Fatalf("early claim: %v %v", due, err)
	}
	if _, due, err := updater.ClaimInterval(ctx, now.Add(17*time.Minute)); err != nil || !due {
		t.Fatalf("due claim: %v %v", due, err)
	}
	rows := serviceregistry.Build(serviceregistry.Facts{Updates: updateFacts(ctx)})
	if rows[9].State != serviceregistry.StateError || rows[9].Detail != serviceregistry.DetailUpdateSourceInvalid {
		t.Fatalf("missing checkout: %+v", rows[9])
	}
	if _, err := s.setSetting(ctx, "updater.source", "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestMainUpdatedStartsSourceInstaller(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	repo := librarianRepo(t)
	if err := (updaterservice.Store{DataDir: store.DataDir()}).Update(t.Context(), func(c *updaterservice.Config) error { c.Source, c.Enabled = repo, true; return nil }); err != nil {
		t.Fatal(err)
	}
	installer := hookinstall.New()
	var calls int
	installer.Exec = func(_ context.Context, p processguard.Process) error {
		if len(p.Args) == 0 {
			t.Fatal("empty installer command")
		}
		if p.Args[0] == "systemd-run" {
			calls++
		} else if p.Args[0] != "systemctl" {
			t.Fatalf("installer command = %v", p.Args)
		} else if p.Args[2] == "is-active" {
			// is-active exits nonzero for an inactive unit: no install runs yet.
			return errors.New("inactive")
		}
		return nil
	}
	s.hooks.Install = &installer
	var events []room.HookEvent
	s.hooks.Post = func(_ hooks.Event, ev room.HookEvent) error { events = append(events, ev); return nil }
	h := s.librarianMainUpdates(s.hooks)
	event := hooks.Event{Event: hooks.MainUpdated, Repo: repo, SHA: strings.Repeat("a", 40)}
	if err := h.MainUpdated(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("installer starts = %d, want 1", calls)
	}
	runs, err := updateRuns()
	if err != nil || len(runs) != 1 || runs[0].Result != UpdatePending {
		t.Fatalf("update runs = %+v, %v", runs, err)
	}
	if err := h.MainUpdated(t.Context(), event); err != nil || calls != 1 {
		t.Fatalf("duplicate start: calls=%d err=%v", calls, err)
	}
	if err := installer.Jobs.Update(runs[0].ID, func(j *hookinstall.Job) error {
		j.StartedAt = time.Now().Add(-4 * time.Minute)
		j.Status, j.Result, j.FinishedAt = "finished", "pass", time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.hooks.ReconcileInstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].State != room.HookRunning || events[1].State != room.HookPassed || events[1].RunID != events[0].RunID || events[1].Event != hooks.MainUpdated {
		t.Fatalf("events = %+v", events)
	}
	log, err := updateRunLog(runs[0].ID)
	if err != nil || log.ExitCode != 0 || !strings.Contains(log.Tail, "Install unit:") {
		t.Fatalf("update log = %+v, %v", log, err)
	}
}
