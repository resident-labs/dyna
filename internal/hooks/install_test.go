package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

func TestInstallReceiptSurvivesRestartAndFinishesHookLog(t *testing.T) {
	withFakeNodeTools(t)
	s, repo := setup(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module dyna-agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "desktop", "build.sh"), []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := SelfUpdateRecipe(ctx, repo, "/fake/dyna", "install")
	if err != nil {
		t.Fatal(err)
	}
	add(t, s, repo, h)
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	id := "33333333333333333333333333333333"
	log := filepath.Join(t.TempDir(), "hook.log")
	workerLog := filepath.Join(t.TempDir(), "worker.log")
	if err = os.WriteFile(log, []byte("Install unit: dyna-install-example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(workerLog, []byte("fake unit build result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ev := Run{ID: id, Repo: r.ID, Name: "hook-install", Result: "pass", LogPath: log, StartedAt: time.Now(), FinishedAt: time.Now()}
	if _, err = s.saveRun(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	installer := s.installer()
	installer.Exec = func(context.Context, processguard.Process) error { return nil }
	s.Install = &installer
	j := hookinstall.Job{ID: id, Unit: "dyna-install-example", Repo: r.ID, Hook: "install", SHA: strings.Repeat("a", 40), HookLog: log, WorkerLog: workerLog, Status: "finished", Result: "error", Cause: "fake build failure", FinishedAt: time.Now(), Config: hookinstall.Config{Main: repo}}
	if err = installer.Jobs.Update(id, func(out *hookinstall.Job) error { *out = j; return nil }); err != nil {
		t.Fatal(err)
	}
	// A fast worker may finish before the launcher marks the hook pending.
	if err = s.ReconcileInstall(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := installer.Jobs.Read(id)
	if before.Recorded {
		t.Fatal("result recorded before handoff")
	}
	if ev, err = s.pendingInstall(t.Context(), ev); err != nil || ev.Result != "pending" {
		t.Fatalf("pending: %#v %v", ev, err)
	}
	notices := 0
	restarted := Service{DataDir: s.DataDir, Install: &installer, Post: func(_ Event, ev room.HookEvent) error {
		if ev.State == room.HookError {
			notices++
		}
		return nil
	}}
	for n := 0; n < 2; n++ {
		if err = restarted.ReconcileInstall(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := restarted.Runs(ctx, repo, "install")
	if err != nil || len(runs) != 1 || runs[0].Result != "error" {
		t.Fatalf("result: %#v %v", runs, err)
	}
	if notices != 1 {
		t.Fatalf("notices: %d", notices)
	}
	// A late parent save must not replace a unit result that recovery recorded.
	ev.Result = "pass"
	if _, err = s.saveRun(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	ev, err = s.pendingInstall(t.Context(), ev)
	if err != nil || ev.Result != "error" {
		t.Fatalf("late handoff: %#v %v", ev, err)
	}

	b, err := os.ReadFile(log)
	if err != nil || strings.Count(string(b), "fake unit build result") != 1 || !strings.Contains(string(b), "fake build failure") {
		t.Fatalf("log: %s %v", b, err)
	}
}

func TestSelfUpdateRecipeCapturesToolsAndRejectsMissingTools(t *testing.T) {
	_, repo := setup(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module dyna-agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "desktop/build.sh"), []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err = os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(bash, filepath.Join(bin, "bash")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err = SelfUpdateRecipe(ctx, repo, "/fake/dyna", "self-update"); err == nil || !strings.Contains(err.Error(), "requires go") {
		t.Fatalf("missing tool: %v", err)
	}
	for _, tool := range []string{"go", "npm", "node"} {
		if err = os.WriteFile(filepath.Join(bin, tool), []byte("fake"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	h, err := SelfUpdateRecipe(ctx, repo, "/fake/dyna", "self-update")
	if err != nil || h.Label != "Update Dyna" || !strings.Contains(h.Command, "--tool-path") || !strings.Contains(h.Command, bin) {
		t.Fatalf("captured tools: %#v %v", h, err)
	}
}

func TestCapturedToolPathDropsComputerBinsAndDuplicates(t *testing.T) {
	data := t.TempDir()
	computers := filepath.Join(data, "computers")
	keep := filepath.Join(data, "computers-extra", "bin")
	tools := t.TempDir()
	for _, dir := range []string{filepath.Join(computers, "session", "bin"), keep} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	entries := []string{filepath.Join(computers, "session", "bin"), tools, tools + "/", computers, "", ".", "relative/bin", filepath.Join(data, "gone"), keep, tools}
	got := capturedToolPath(entries, data)
	want := strings.Join([]string{tools, keep}, string(os.PathListSeparator))
	if got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

// withFakeNodeTools appends stub npm and node commands to PATH. The
// self-update recipe resolves go, npm and node when it is added; CI's Go
// container has no Node tools, and the recipe never runs them in tests.
func withFakeNodeTools(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"npm", "node"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
}

// A unit that passes without a warning still ends the run the hook started.
// The hook hands its result to the unit, so nothing else would close the card.
func TestPassedInstallUnitEndsTheRunCard(t *testing.T) {
	withFakeNodeTools(t)
	s, repo := setup(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module dyna-agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "desktop", "build.sh"), []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := SelfUpdateRecipe(ctx, repo, "/fake/dyna", "install")
	if err != nil {
		t.Fatal(err)
	}
	add(t, s, repo, h)
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	id := "44444444444444444444444444444444"
	logPath := filepath.Join(t.TempDir(), "hook.log")
	if err = os.WriteFile(logPath, []byte("Install unit: dyna-install-example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ev := Run{ID: id, Repo: r.ID, Name: "hook-install", Result: "pending", LogPath: logPath, StartedAt: time.Now().Add(-2 * time.Second)}
	if _, err = s.saveRun(ctx, ev); err != nil {
		t.Fatal(err)
	}
	installer := s.installer()
	s.Install = &installer
	j := hookinstall.Job{ID: id, Unit: "dyna-install-example", Repo: r.ID, Hook: "install", SHA: strings.Repeat("b", 40), HookLog: logPath, Status: "finished", Result: "pass", FinishedAt: time.Now(), Config: hookinstall.Config{Main: repo}}
	if err = installer.Jobs.Update(id, func(out *hookinstall.Job) error { *out = j; return nil }); err != nil {
		t.Fatal(err)
	}
	var got []room.HookEvent
	s.Post = func(_ Event, ev room.HookEvent) error { got = append(got, ev); return nil }
	if err = s.ReconcileInstall(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != room.HookPassed || got[0].RunID != id || got[0].Hook != "install" {
		t.Fatalf("events = %+v", got)
	}
	if got[0].DurationMS <= 0 {
		t.Fatalf("the passing result has no duration: %+v", got[0])
	}
}
