package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
)

// noBoundaryWait is steering.maxToolWait for fake CLIs that print no output
// dialect: dyna cannot see their tool calls, so it interrupts them after this.
const noBoundaryWait = 20 * time.Millisecond

func TestObservedBufferTeesLiveStdout(t *testing.T) {
	var live bytes.Buffer
	stdout := newObservedBuffer(nil, &live)
	_, _ = stdout.Write([]byte("first line\npartial"))
	_, _ = stdout.Write([]byte(" line\n"))
	if got, want := live.String(), "first line\npartial line\n"; got != want {
		t.Fatalf("live stdout = %q, want %q", got, want)
	}
	if got := stdout.String(); got != live.String() {
		t.Fatalf("retained stdout = %q, live = %q", got, live.String())
	}
}

func TestObservedBufferFlushesPendingOnlyToExplicitSideChannel(t *testing.T) {
	var observed, pending []string
	stdout := newObservedBuffer(func(line string) { observed = append(observed, line) }, nil)
	_, _ = stdout.Write([]byte("complete\nunterminated"))
	stdout.FlushPending(func(line string) { pending = append(pending, line) })
	if len(observed) != 1 || observed[0] != "complete" {
		t.Fatalf("live observations = %#v, want only complete records", observed)
	}
	if len(pending) != 1 || pending[0] != "unterminated" {
		t.Fatalf("pending side-channel records = %#v", pending)
	}
	if got := stdout.String(); got != "complete\nunterminated" {
		t.Fatalf("retained stdout = %q", got)
	}
}

func TestCancellationDuringNativeJournalSteerNeverResumes(t *testing.T) {
	journalPath := createHarnessJournal(t, 37)
	p, logPath := installFakeCodex(t, fakeCodexHold)
	p.Name = "cancel-journal-steer"
	p.Env[runstore.AgentJournalEnv] = journalPath
	// Codex receives idle journal reminders as turn/steer. The fake holds that
	// steer, so cancellation lands while the steer request is still in flight.
	steers := fakeCodexGate(t, p.Env, envFakeCodexSteerGate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := RunWithJournal(ctx, p, "cancel during flush", t.TempDir(), true, JournalOptions{
			Path: journalPath, IdleAfter: 30 * time.Millisecond, MaxToolWait: noBoundaryWait,
		})
		done <- outcome{result: result, err: err}
	}()
	held := awaitDoorbell(t, "the live input channel", steers)
	cancel()
	// Release the steer only after cancellation. Otherwise the interrupt waits
	// behind the held steer until its timeout.
	held.Close()
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "canceled/timed out") || got.result.JournalNudges != 0 {
			t.Fatalf("cancellation outcome = %#v, %v", got.result, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancellation during native journal steer did not stop the worker")
	}
	log := readTestFile(t, logPath)
	if fakeCodexSpawns(log) != 1 || fakeCodexRequests(log, "thread/resume") != 0 {
		t.Fatalf("canceled journal steer resumed the session:\n%s", log)
	}
}

func TestJournalSupervisorValidEntryResetsInactivity(t *testing.T) {
	journalPath := createHarnessJournal(t, 2)
	var entries []runstore.AgentJournalEntry
	supervisor := newJournalSupervisor(JournalOptions{
		Path:      journalPath,
		IdleAfter: time.Minute,
		OnEntry:   func(entry runstore.AgentJournalEntry) { entries = append(entries, entry) },
	})
	supervisor.state.lastActivity = time.Now().Add(-2 * time.Minute)
	if !supervisor.reminderDue() {
		t.Fatal("precondition: inactivity reminder is not due")
	}
	oldActivity := supervisor.state.lastActivity
	if err := runstore.AppendAgentJournalPath(journalPath, runstore.AgentJournalEntry{
		Kind: "verification", Message: "Focused checks passed.", Next: "Continue implementation.", Source: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	supervisor.poll()
	if supervisor.reminderDue() {
		t.Fatal("valid complete agent entry did not reset the inactivity deadline")
	}
	if !supervisor.state.lastActivity.After(oldActivity) || supervisor.state.reminded || supervisor.state.unavailableNote {
		t.Fatalf("supervisor state after activity = %#v", supervisor)
	}
	if len(entries) != 1 || entries[0].Kind != "verification" {
		t.Fatalf("OnEntry entries = %#v", entries)
	}
}

func TestJournalStateKeepsCommittedOffsetAcrossHarnessTurns(t *testing.T) {
	journalPath := createHarnessJournal(t, 34)
	state := NewJournalState()
	first := newJournalSupervisor(JournalOptions{Path: journalPath, IdleAfter: time.Minute, State: state})
	initialOffset := first.offset
	appendRawJournalTest(t, journalPath, `{"ts":1,"kind":"finding","message":"spans attempts","source":"agent"`)
	first.poll()
	if first.offset != initialOffset {
		t.Fatalf("partial record advanced offset to %d, want %d", first.offset, initialOffset)
	}

	var entries []runstore.AgentJournalEntry
	second := newJournalSupervisor(JournalOptions{
		Path: journalPath, IdleAfter: time.Minute, State: state,
		OnEntry: func(entry runstore.AgentJournalEntry) { entries = append(entries, entry) },
	})
	if second.offset != initialOffset {
		t.Fatalf("next harness turn offset = %d, want committed %d", second.offset, initialOffset)
	}
	appendRawJournalTest(t, journalPath, "}\n")
	second.poll()
	if len(entries) != 1 || entries[0].Message != "spans attempts" || !second.hasAgentEntry() {
		t.Fatalf("completed cross-turn entry = %#v", entries)
	}
}

func TestJournalStateKeepsIdleDeadlineAcrossHarnessTurns(t *testing.T) {
	journalPath := createHarnessJournal(t, 36)
	p := fakeworker.InstallClaude(t, `sleep 0.04; printf ok`)
	p.Name = "quiet-state"
	p.ExtraArgs = []string{"--no-session-persistence"}
	state := NewJournalState()
	var nudges []JournalNudge
	opts := JournalOptions{
		Path: journalPath, IdleAfter: 60 * time.Millisecond, State: state,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	}
	for i := 0; i < 2; i++ {
		if _, err := RunWithJournal(context.Background(), p, "schema turn", t.TempDir(), true, opts); err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
	}
	idle := 0
	for _, nudge := range nudges {
		if nudge.Reason == JournalNudgeIdle {
			idle++
		}
	}
	if idle != 1 {
		t.Fatalf("idle reminders = %d in %#v, want one deadline shared across turns", idle, nudges)
	}
}

func TestJournalSupervisorIgnoresNonEntries(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*testing.T, string)
		wantSameOffset bool
	}{
		{
			name: "touch",
			mutate: func(t *testing.T, path string) {
				now := time.Now().Add(time.Second)
				if err := os.Chtimes(path, now, now); err != nil {
					t.Fatal(err)
				}
			},
			wantSameOffset: true,
		},
		{
			name:   "malformed complete record",
			mutate: func(t *testing.T, path string) { appendRawJournalTest(t, path, "not-json\n") },
		},
		{
			name: "partial valid-looking record",
			mutate: func(t *testing.T, path string) {
				appendRawJournalTest(t, path, `{"ts":1,"kind":"update","message":"unfinished","source":"agent"}`)
			},
			wantSameOffset: true,
		},
		{
			name: "invalid complete agent record",
			mutate: func(t *testing.T, path string) {
				appendRawJournalTest(t, path, "{\"ts\":0,\"kind\":\"update\",\"message\":\"invalid timestamp\",\"source\":\"agent\"}\n")
			},
		},
		{
			name: "system completion",
			mutate: func(t *testing.T, path string) {
				if err := runstore.AppendAgentJournalPath(path, runstore.AgentJournalEntry{
					Kind: "complete", Message: "Agent completed.", Source: "system",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journalPath := createHarnessJournal(t, i+10)
			entryCalls := 0
			supervisor := newJournalSupervisor(JournalOptions{
				Path: journalPath, IdleAfter: time.Minute,
				OnEntry: func(runstore.AgentJournalEntry) { entryCalls++ },
			})
			initialOffset := supervisor.offset
			supervisor.state.lastActivity = time.Now().Add(-2 * time.Minute)
			tt.mutate(t, journalPath)
			supervisor.poll()

			if !supervisor.reminderDue() {
				t.Fatal("non-entry incorrectly reset the inactivity deadline")
			}
			if entryCalls != 0 {
				t.Fatalf("OnEntry calls = %d, want 0", entryCalls)
			}
			if tt.wantSameOffset && supervisor.offset != initialOffset {
				t.Fatalf("offset = %d, want unchanged %d", supervisor.offset, initialOffset)
			}
		})
	}
}

func TestQuietWorkerGetsItsRemindersThroughItsSession(t *testing.T) {
	journalPath := createHarnessJournal(t, 30)
	logPath := filepath.Join(t.TempDir(), "calls.log")
	p := fakeworker.InstallClaude(t, `
printf 'CALL %s\n' "$PPID" >> "$DYNA_FAKE_LOG"
sleep 0.15
printf 'FINISHED\n' >> "$DYNA_FAKE_LOG"
printf '%s' 'worker completed'
`)
	p.Name = "quiet-worker"
	p.Env = mergedEnv(p.Env, map[string]string{"DYNA_FAKE_LOG": logPath})
	var deliveries []JournalNudge
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := RunWithJournal(ctx, p, "quiet task", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: 30 * time.Millisecond,
		OnNudge: func(nudge JournalNudge) { deliveries = append(deliveries, nudge) },
	})
	if err != nil {
		t.Fatalf("RunWithJournal() error = %v", err)
	}
	if result.Output != "worker completed" || result.Nudged {
		t.Fatalf("result = %#v", result)
	}
	reasons := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		reasons = append(reasons, delivery.Reason)
	}
	if !slices.Contains(reasons, JournalNudgeIdle) || !slices.Contains(reasons, JournalNudgeMissing) {
		t.Fatalf("OnNudge deliveries = %#v, want idle and missing-entry reminders", deliveries)
	}
	// The reminder interrupts and continues the exact session, which a stream
	// harness runs in a new process; the turn result survives both.
	log := readTestFile(t, logPath)
	pids := map[string]bool{}
	for _, line := range strings.Split(log, "\n") {
		if pid, ok := strings.CutPrefix(line, "CALL "); ok {
			pids[pid] = true
		}
	}
	if len(pids) != 2 || !strings.Contains(log, "FINISHED") {
		t.Fatalf("the idle reminder did not continue the exact session (%d runs):\n%s", len(pids), log)
	}
}

func TestCancellationWinsWithoutJournalContinuation(t *testing.T) {
	journalPath := createHarnessJournal(t, 31)
	p, logPath := installFakeCodex(t, fakeCodexHold)
	p.Name = "cancel-codex"
	cwd := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	nudgeCalls := 0
	go func() {
		// OnStart runs after turn/start is answered, so cancellation hits a live turn.
		result, err := RunWithOptions(ctx, p, "cancel me", cwd, true, JournalOptions{
			Path: journalPath, IdleAfter: 5 * time.Second,
			OnNudge: func(JournalNudge) { nudgeCalls++ },
		}, SteeringOptions{Source: &armedSource{}, OnStart: cancel}, SessionOptions{}, ExecutionOptions{})
		done <- outcome{result: result, err: err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunWithJournal did not return after cancellation")
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "canceled/timed out") {
		t.Fatalf("error = %v, want cancellation", got.err)
	}
	if got.result.Nudged || got.result.JournalNudges != 0 || nudgeCalls != 0 {
		t.Fatalf("cancellation triggered continuation: result=%#v OnNudge=%d", got.result, nudgeCalls)
	}
	log := readTestFile(t, logPath)
	if fakeCodexSpawns(log) != 1 || fakeCodexRequests(log, "thread/resume") != 0 || fakeCodexRequests(log, "turn/start") != 1 {
		t.Fatalf("canceled worker was continued:\n%s", log)
	}
}

func TestCodexFailureWithoutSessionDoesNotRetry(t *testing.T) {
	p, logPath := installFakeCodex(t, "normal")
	// Thread start fails, so the run never gets a session to resume.
	p.Env[envFakeCodexFailStart] = "1"

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "fake thread start failed") {
		t.Fatalf("Run() error = %v, want the thread start error", err)
	}
	if result.Nudged {
		t.Fatalf("Run() unexpectedly nudged without a session: %#v", result)
	}
	if got := fakeCodexSpawns(readTestFile(t, logPath)); got != 1 {
		t.Fatalf("spawn count = %d, want 1", got)
	}
}

func TestRecoveryBudgetCanDisableNudge(t *testing.T) {
	p, logPath := installFakeCodex(t, "normal")
	p.Env[envFakeCodexFailTurns] = "1"
	p.Env[envFakeCodexFailMessages] = "temporary failure"

	result, err := RunWithRecovery(context.Background(), p, "task", t.TempDir(), false)
	if err == nil || result.Nudged {
		t.Fatalf("RunWithRecovery() = %#v, %v; want unrecovered failure", result, err)
	}
	if got := fakeCodexRequests(readTestFile(t, logPath), "turn/start"); got != 1 {
		t.Fatalf("turn count = %d, want 1", got)
	}
}

func TestCodexCancellationDoesNotNudge(t *testing.T) {
	p, logPath := installFakeCodex(t, fakeCodexHold)
	p.Name = "sol-max"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// OnStart runs after turn/start is answered, so cancellation hits a live
	// turn. OnStart needs a steering source; this one stays idle.
	result, err := RunWithOptions(ctx, p, "task", t.TempDir(), true, JournalOptions{}, SteeringOptions{Source: &armedSource{}, OnStart: cancel}, SessionOptions{}, ExecutionOptions{})
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("OnStart never canceled the live turn")
	}
	if err == nil || !strings.Contains(err.Error(), "canceled/timed out") {
		t.Fatalf("Run() error = %v, want timeout", err)
	}
	if result.Nudged {
		t.Fatalf("Run() nudged after cancellation: %#v", result)
	}
	if got := fakeCodexRequests(readTestFile(t, logPath), "turn/start"); got != 1 {
		t.Fatalf("turn count = %d, want 1", got)
	}
}

func TestContextTerminationDoesNotLookLikeEmptyOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := attemptError(ctx, profile.Profile{Name: "luna"}, []string{"codex"}, attempt{
		started: true, contextDone: true,
	})
	if err == nil || !strings.Contains(err.Error(), "canceled/timed out") {
		t.Fatalf("attemptError() = %v, want cancellation/timeout", err)
	}
	if strings.Contains(err.Error(), "empty output") {
		t.Fatalf("attemptError() hid context termination as an empty response: %v", err)
	}
}

// Stopping a turn cancels its steering read too; the stop is the cause.
func TestStoppedTurnIsNotASteeringFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := attemptError(ctx, profile.Profile{Name: "luna"}, []string{"codex"}, attempt{
		started: true, contextDone: true, steeringErr: context.Canceled,
	})
	if err == nil || !strings.Contains(err.Error(), "canceled/timed out") || strings.Contains(err.Error(), "steering source failed") {
		t.Fatalf("attemptError() = %v, want cancellation", err)
	}
	err = attemptError(context.Background(), profile.Profile{Name: "luna"}, []string{"codex"}, attempt{
		started: true, steeringErr: errors.New("read steering"),
	})
	if err == nil || !strings.Contains(err.Error(), "steering source failed") {
		t.Fatalf("attemptError() = %v, want steering failure", err)
	}
}

func TestMissingWorkingDirectoryHasDistinctError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "removed-worktree")
	p := fakeworker.InstallClaude(t, "printf should-not-run")
	p.Name = "missing-cwd"

	_, err := runTest(context.Background(), p, "task", missing)
	if err == nil {
		t.Fatal("Run() succeeded with a missing working directory")
	}
	if !strings.Contains(err.Error(), "worker missing-cwd cannot start") ||
		!strings.Contains(err.Error(), "working directory "+strconv.Quote(missing)+" is unavailable") {
		t.Fatalf("Run() error = %v, want a distinct error naming the working directory", err)
	}
	if strings.Contains(err.Error(), "fork/exec") || strings.Contains(err.Error(), "(/bin/sh) failed") {
		t.Fatalf("Run() error still looks like an ambiguous binary spawn failure: %v", err)
	}
}

func TestWorkerEnvPWDMatchesWorkingDirectory(t *testing.T) {
	t.Setenv("PWD", "/somewhere/else")
	t.Setenv("OLDPWD", "/somewhere/older")
	dir := t.TempDir()
	p := fakeworker.InstallClaude(t, `printf '%s|%s' "$PWD" "${OLDPWD:-unset}"`)
	p.Name = "pwd-env"

	result, err := runTest(context.Background(), p, "task", dir)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	pwd, oldpwd, _ := strings.Cut(result.Output, "|")
	if pwd != dir && pwd != resolved {
		t.Fatalf("worker $PWD = %q, want working directory %q", pwd, dir)
	}
	if oldpwd != "unset" {
		t.Fatalf("worker $OLDPWD = %q, want unset", oldpwd)
	}
}

func TestCodexReportsBothFailures(t *testing.T) {
	p, logPath := installFakeCodex(t, "normal")
	p.Name = "sol-max"
	p.Env[envFakeCodexFailTurns] = "2"
	p.Env[envFakeCodexFailMessages] = "first capacity failure|capacity persists"

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "first capacity failure") || !strings.Contains(err.Error(), "same-session nudge failed") || !strings.Contains(err.Error(), "capacity persists") {
		t.Fatalf("Run() error = %v, want both diagnostics", err)
	}
	if !result.Nudged {
		t.Fatalf("Run() result = %#v, want attempted nudge", result)
	}
	if got := fakeCodexRequests(readTestFile(t, logPath), "turn/start"); got != 2 {
		t.Fatalf("turn count = %d, want 2", got)
	}
}

func TestExplicitControlsDisableNudge(t *testing.T) {
	tests := []struct {
		name    string
		profile profile.Profile
	}{
		{"claude", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--no-session-persistence"}}},
		{"claude-budget", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--max-budget-usd", "1"}}},
		{"claude-fork", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--fork-session"}}},
		{"codex", profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: []string{"--ephemeral"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := buildInvocation(tt.profile, "task")
			if err != nil {
				t.Fatalf("buildInvocation() error = %v", err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			if inv.sessionID != nil || inv.resume != nil {
				t.Fatalf("explicitly controlled invocation is resumable: %#v", inv.initial.argv)
			}
		})
	}
}

func TestCodexSandboxAndDirArgsStayResumable(t *testing.T) {
	for _, extra := range [][]string{
		{"--sandbox", "read-only"},
		{"--cd=/tmp/project"},
		{"--add-dir", "/tmp/other"},
	} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: extra}, "task")
		if err != nil {
			t.Fatal(err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		if inv.resume == nil || inv.sessionID == nil {
			t.Fatalf("Codex args %#v lost exact-session resume", extra)
		}
	}
}

func TestCodexProfileAndProviderArgsAreRejected(t *testing.T) {
	for _, extra := range [][]string{
		{"--profile", "locked-down"},
		{"-p", "locked-down"},
		{"--oss"},
		{"--local-provider", "ollama"},
	} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: extra}, "task")
		if inv.cleanup != nil {
			inv.cleanup()
		}
		if err == nil || !strings.Contains(err.Error(), "does not support --profile/--oss/--local-provider") {
			t.Fatalf("buildInvocation(%v) error = %v, want the unsupported provider error", extra, err)
		}
	}
}

func TestCodexPermissionPolicyParams(t *testing.T) {
	for _, tc := range []struct {
		name     string
		p        profile.Profile
		approval string
		sandbox  string
	}{
		{name: "safe mode requests read-only", p: profile.Profile{Harness: profile.HarnessCodex, SafeMode: true}, approval: "on-request", sandbox: "read-only"},
		{name: "default bypasses", p: profile.Profile{Harness: profile.HarnessCodex}, approval: "never", sandbox: "danger-full-access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := buildInvocation(tc.p, "task")
			if err != nil {
				t.Fatal(err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			thread := inv.initial.codexPolicy.params(true)
			if thread["approvalPolicy"] != tc.approval || thread["sandbox"] != tc.sandbox {
				t.Fatalf("thread params = %#v, want approval %q and sandbox %q", thread, tc.approval, tc.sandbox)
			}
			turn, _ := inv.initial.codexPolicy.params(false)["sandboxPolicy"].(map[string]any)
			if want := map[string]string{"read-only": "readOnly", "danger-full-access": "dangerFullAccess"}[tc.sandbox]; turn["type"] != want {
				t.Fatalf("turn sandboxPolicy = %#v, want type %q", turn, want)
			}
		})
	}
}

// A custom permission profile owns the sandbox, so dyna sends no overrides.
func TestCodexCustomPermissionConfigOmitsOverrides(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, SafeMode: true, ExtraArgs: []string{"-c", `default_permissions="dyna-journal"`}}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if inv.cleanup != nil {
		defer inv.cleanup()
	}
	if got := inv.initial.codexPolicy.params(true); len(got) != 0 {
		t.Fatalf("custom permission config sent overrides: %#v", got)
	}
}

func TestReadOnlyCodexJournalUsesNarrowResumablePermissionProfile(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "wf_permissions", "agents", "7", "journal.jsonl")
	for _, extra := range [][]string{
		{"--sandbox", "read-only", "-c", "model_reasoning_effort=high"},
		{"--sandbox=read-only"},
		{"-s", "read-only"},
		{"-c", `sandbox_mode="read-only"`},
		{"--config", `default_permissions=":read-only"`},
	} {
		p, err := withJournalPermissions(profile.Profile{
			Name: "readonly", Harness: profile.HarnessCodex, ExtraArgs: extra,
		}, journalPath)
		if err != nil {
			t.Fatalf("withJournalPermissions(%v) error = %v", extra, err)
		}
		if !p.SafeMode || hasAnyOption(p.ExtraArgs, "--sandbox", "-s") || hasArg(p.ExtraArgs, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("read-only conversion weakened or retained conflicting flags: %#v", p)
		}
		joined := strings.Join(p.ExtraArgs, " ")
		if !strings.Contains(joined, `default_permissions="dyna-journal"`) ||
			!strings.Contains(joined, `approval_policy="never"`) ||
			!strings.Contains(joined, `extends=":read-only"`) ||
			!strings.Contains(joined, strconv.Quote(filepath.Dir(journalPath))+`="write"`) {
			t.Fatalf("narrow journal permission config missing from %#v", p.ExtraArgs)
		}

		inv, err := buildInvocation(p, "inspect only")
		if err != nil {
			t.Fatal(err)
		}
		if inv.resume == nil || inv.sessionID == nil {
			t.Fatalf("converted read-only invocation is not exact-session resumable: %#v", inv.initial.argv)
		}
		resumed := inv.resume("thread-readonly", "continue")
		if strings.Contains(strings.Join(inv.initial.argv, " "), "dangerously-bypass") ||
			strings.Contains(strings.Join(resumed.argv, " "), "dangerously-bypass") ||
			!strings.Contains(strings.Join(resumed.argv, " "), `default_permissions="dyna-journal"`) {
			t.Fatalf("permission profile was not preserved safely on resume: initial=%#v resume=%#v", inv.initial.argv, resumed.argv)
		}
	}
}

func TestExplicitHarnessPermissionsAreNeverAutoBypassed(t *testing.T) {
	tests := []profile.Profile{
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"--sandbox", "read-only"}},
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"-c", `default_permissions="locked-down"`}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--permission-mode", "plan"}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--allowedTools", "Read,Grep"}},
	}
	for _, p := range tests {
		inv, err := buildInvocation(p, "inspect only")
		if err != nil {
			t.Fatalf("buildInvocation(%#v): %v", p, err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		joined := strings.Join(inv.initial.argv, " ")
		if strings.Contains(joined, "dangerously-bypass-approvals-and-sandbox") || strings.Contains(joined, "dangerously-skip-permissions") {
			t.Fatalf("explicit permissions were weakened: %s", joined)
		}
	}
}

// The private runtime adds a generated --settings file to every Claude run.
// A settings file is not a permission choice, so headless workers keep bypass.
func TestClaudeSettingsFileKeepsPermissionBypass(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--settings", settings, "--setting-sources", ""}}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if inv.cleanup != nil {
		defer inv.cleanup()
	}
	if !strings.Contains(strings.Join(inv.initial.argv, " "), "--dangerously-skip-permissions") {
		t.Fatalf("settings file disabled the permission bypass: %v", inv.initial.argv)
	}
}

func TestCodexReservedOutputArgsAreRejected(t *testing.T) {
	for _, extra := range [][]string{
		{"-o", "/tmp/result"},
		{"--output-last-message=/tmp/result"},
		{"-o=/tmp/result"},
		{"-o/tmp/result"},
	} {
		if inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: extra}, "task"); err == nil {
			if inv.cleanup != nil {
				inv.cleanup()
			}
			t.Fatalf("buildInvocation(%v) accepted reserved output argument", extra)
		}
	}
}

func TestMockReportsCallerTaskWithoutJournalEnvelope(t *testing.T) {
	out, err := runMock(context.Background(), "[DYNA WORK JOURNAL]\ninternal instructions\n[/DYNA WORK JOURNAL]\n\n[ORIGINAL TASK]\ninspect the decoder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inspect the decoder") || strings.Contains(out, "DYNA WORK JOURNAL") {
		t.Fatalf("mock output leaked internal envelope: %q", out)
	}
}

func TestMuseInvocationIsResumableAndParsesTerminalText(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{
		Name: "wonder", Harness: profile.HarnessMuse, Model: "muse-wonder-1.0",
		ExtraArgs: []string{"--reasoning-effort", "high"},
	}, "do the task")
	if err != nil {
		t.Fatal(err)
	}
	if inv.sessionID == nil || inv.resume == nil || inv.initial.parseOutput == nil {
		t.Fatalf("muse invocation is not resumable: %#v", inv.initial.argv)
	}
	joined := strings.Join(inv.initial.argv, " ")
	for _, want := range []string{"muse serve", "--disable-sandbox", "--model muse-wonder-1.0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s: %v", want, inv.initial.argv)
		}
	}
	sessionID := inv.initial.museSession
	if sessionID == "" || inv.initial.prompt != "do the task" || inv.initial.museReasoning != "high" {
		t.Fatalf("wrong MSP request: %+v", inv.initial)
	}
	resumed := inv.resume(sessionID, "continue please")
	if resumed.museSession != sessionID || !resumed.museResume || resumed.prompt != "continue please" {
		t.Fatalf("wrong continuation: %+v", resumed)
	}
	stdout := strings.Join([]string{
		`{"payload_type":"turn.input.user","stream":{"kind":"session","id":"` + sessionID + `"},"payload":{"prompt":"do the task"}}`,
		`{"payload_type":"run.output.delta","stream":{"kind":"session","id":"` + sessionID + `"},"payload":{"text":"all done"}}`,
		`{"payload_type":"run.terminal.completed","stream":{"kind":"session","id":"` + sessionID + `"},"payload":{"terminal":"completed","text":"all done"}}`,
	}, "\n")
	if got := inv.initial.parseOutput(stdout); got != "all done" {
		t.Fatalf("parseOutput = %q, want the terminal text, not raw records", got)
	}
	if got := museSessionID(stdout); got != sessionID {
		t.Fatalf("museSessionID = %q, want %q", got, sessionID)
	}
}

func TestMuseRejectsReservedExtraArgs(t *testing.T) {
	for _, extra := range [][]string{{"--json"}, {"--session-id", "x"}, {"--model", "y"}, {"--output-schema", "s.json"}} {
		if _, err := buildInvocation(profile.Profile{Harness: profile.HarnessMuse, ExtraArgs: extra}, "task"); err == nil {
			t.Fatalf("accepted reserved muse extraArgs %v", extra)
		}
	}
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessMuse, SafeMode: true}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(inv.initial.argv, " "), "--yolo") {
		t.Fatalf("safe-mode muse invocation bypassed permissions: %#v", inv.initial.argv)
	}
}

func TestOpenCodeExplicitJSONRemainsResumable(t *testing.T) {
	for _, extra := range [][]string{{"--format", "json"}, {"--format=json"}} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessOpenCode, ExtraArgs: extra}, "task")
		if err != nil {
			t.Fatalf("buildInvocation(%v) error = %v", extra, err)
		}
		if inv.sessionID == nil || inv.resume == nil || inv.initial.parseOutput == nil {
			t.Fatalf("explicit JSON invocation is not resumable: %#v", inv.initial.argv)
		}
	}
}

func TestAssignedSessionIDsAreUnique(t *testing.T) {
	first, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode}, "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode}, "two")
	if err != nil {
		t.Fatal(err)
	}
	a := optionValue(first.initial.argv, "--session-id")
	b := optionValue(second.initial.argv, "--session-id")
	if a == "" || b == "" || a == b {
		t.Fatalf("assigned session IDs = %q, %q; want distinct non-empty IDs", a, b)
	}
}

func TestLeafExecutionNativeControlsInitialAndResume(t *testing.T) {
	tests := []struct {
		name    string
		profile profile.Profile
		option  string
		value   string
	}{
		{"claude", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--verbose"}}, "--disallowedTools", "Agent"},
		{"codex", profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: []string{"-c", "model_reasoning_effort=high"}}, "--disable", "multi_agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := buildInvocationWithExecution(tt.profile, "task", ExecutionOptions{DisableNativeSubagents: true})
			if err != nil {
				t.Fatal(err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			if !hasOptionValue(inv.initial.argv, tt.option, tt.value) || inv.resume == nil {
				t.Fatalf("initial argv/control or resume missing: %#v", inv.initial.argv)
			}
			resumed := inv.resume("session-1", "continue")
			if !hasOptionValue(resumed.argv, tt.option, tt.value) {
				t.Fatalf("resume argv missing control: %#v", resumed.argv)
			}
			if optionIndex(inv.initial.argv, tt.option) <= optionIndex(inv.initial.argv, tt.profile.ExtraArgs[0]) {
				t.Fatalf("native control must follow extra args: %#v", inv.initial.argv)
			}
		})
	}
}

func TestNativeFeatureControlsCoverKnownHarnesses(t *testing.T) {
	// Dyna owns native delegation on every invocation.
	for _, p := range []profile.Profile{
		{Harness: profile.HarnessClaudeCode},
		{Harness: profile.HarnessCodex},
		{Harness: profile.HarnessOpenCode},
	} {
		inv, err := buildInvocation(p, "task")
		if err != nil {
			t.Fatal(err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		joined := strings.Join(inv.initial.argv, " ")
		if p.Harness == profile.HarnessClaudeCode && !hasOptionValue(inv.initial.argv, "--disallowedTools", "Agent") || p.Harness == profile.HarnessCodex && !hasOptionValue(inv.initial.argv, "--disable", "multi_agent") {
			t.Fatalf("profile %#v lacks its native feature policy: %s", p, joined)
		}
	}
	// With the leaf option, harnesses without a known control still get none.
	for _, p := range []profile.Profile{
		{Harness: profile.HarnessOpenCode},
	} {
		inv, err := buildInvocationWithExecution(p, "task", ExecutionOptions{DisableNativeSubagents: true})
		if err != nil {
			t.Fatal(err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		joined := strings.Join(inv.initial.argv, " ")
		if strings.Contains(joined, "--disallowedTools Agent") || strings.Contains(joined, "--disable multi_agent") {
			t.Fatalf("profile %#v received an unsupported native control: %s", p, joined)
		}
	}
}

func TestLeafExecutionRejectsExplicitConflicts(t *testing.T) {
	leaf := ExecutionOptions{DisableNativeSubagents: true}
	for _, p := range []profile.Profile{
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--allowedTools", "Read,Agent"}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--allowed-tools", "Workflow"}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--allowedTools", "Read", "Agent(reviewer)"}},
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"--enable", "multi_agent"}},
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"-c", "features.multi_agent=true"}},
	} {
		if inv, err := buildInvocationWithExecution(p, "task", leaf); err == nil {
			if inv.cleanup != nil {
				inv.cleanup()
			}
			t.Fatalf("accepted conflicting leaf profile: %#v", p)
		}
		// A generic invocation can still use the profile without this restriction.
		inv, err := buildInvocation(p, "task")
		if err != nil {
			t.Fatalf("normal invocation rejected %#v: %v", p, err)
		}
		if inv.cleanup != nil {
			inv.cleanup()
		}
	}
}

func TestLeafExecutionMergesClaudeDenyLists(t *testing.T) {
	p := profile.Profile{
		Harness:   profile.HarnessClaudeCode,
		ExtraArgs: []string{"--disallowedTools", "Bash(git *)", "--disallowed-tools=WebFetch"},
	}
	inv, err := buildInvocationWithExecution(p, "task", ExecutionOptions{DisableNativeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ option, value string }{
		{"--disallowedTools", "Bash"},
		{"--disallowedTools", "Agent"},
		{"--disallowedTools", "Workflow"},
		{"--disallowedTools", "WebFetch"},
		{"--disallowedTools", "Agent"},
		{"--disallowedTools", "Workflow"},
	} {
		if !hasOptionValue(inv.initial.argv, check.option, check.value) {
			t.Fatalf("merged argv missing %s=%s: %#v", check.option, check.value, inv.initial.argv)
		}
	}
}

func optionIndex(args []string, option string) int {
	for i, arg := range args {
		if arg == option || strings.HasPrefix(arg, option+"=") {
			return i
		}
	}
	return -1
}

func createHarnessJournal(t *testing.T, agentID int) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	run := &runstore.Run{Dir: filepath.Join(runstore.RunsDir(), "wf_harness-journal")}
	if err := os.MkdirAll(run.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := run.StartAgentJournal(agentID, "test worker", "test-profile", "Tests", "test prompt")
	if err != nil {
		t.Fatalf("StartAgentJournal() error = %v", err)
	}
	return path
}

func appendRawJournalTest(t *testing.T, path, record string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(record); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func installFakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := fsutil.WriteExecutable(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(dir, "calls.log")
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func optionValue(args []string, option string) string {
	for i, arg := range args {
		if arg == option && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, option+"=") {
			return strings.TrimPrefix(arg, option+"=")
		}
	}
	return ""
}

func runTest(ctx context.Context, p profile.Profile, prompt, cwd string) (Result, error) {
	return run(ctx, p, prompt, cwd, true, 0)
}

func TestMockIgnoresTrailingWorkerContextAfterTaskAndSchema(t *testing.T) {
	const prefix = "[DYNA SESSION]\nshared instructions\n\n[ORIGINAL TASK]\nRESPOND: exact task result"
	for _, schema := range []string{"", "\n\n---\nOUTPUT FORMAT: schema instructions"} {
		prompt := prefix + schema + "\n\n[DYNA WORKER CONTEXT]\nunique worker metadata\n[/DYNA WORKER CONTEXT]"
		if got := mockResponse(prompt); got != "exact task result" {
			t.Fatalf("mock leaked worker context: %q", got)
		}
	}
}

func TestClaudeScopedDenialsStillAddBlanketDenials(t *testing.T) {
	for _, extra := range [][]string{
		{"--disallowedTools", "Agent(reviewer),Workflow(build),Read"},
		{"--disallowed-tools=Agent(reviewer),Workflow(build),Read"},
		{"--disallowedTools", "Agent(reviewer)", "Workflow(build)", "Read"},
	} {
		inv, err := buildInvocationWithExecution(profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: extra}, "task", ExecutionOptions{DisableNativeSubagents: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range []commandSpec{inv.initial, inv.resume("session-1", "continue")} {
			joined := strings.Join(spec.argv, " ")
			if !strings.Contains(joined, "Agent(reviewer)") || !strings.Contains(joined, "Workflow(build)") || !strings.Contains(joined, "Read") {
				t.Fatalf("existing denial lost: %v", spec.argv)
			}
			// hasOptionValue also matches scoped rules, so check exact tokens here.
			var tokens []string
			for _, arg := range spec.argv {
				_, value, found := strings.Cut(arg, "=")
				if !found {
					value = arg
				}
				tokens = append(tokens, strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })...)
			}
			for _, tool := range []string{"Agent", "Workflow"} {
				count := 0
				for _, token := range tokens {
					if token == tool {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("blanket %s denial count = %d: %v", tool, count, spec.argv)
				}
			}
		}
	}
}

func TestModelCooldownSelection(t *testing.T) {
	storetest.DataDir(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	p := profile.Profile{Name: "chain", Model: "a", Models: []string{"a", "b", "c"}}
	check := func(at time.Time, want string) {
		t.Helper()
		first, models := SelectHealthyModel(p, at)
		if strings.Join(models, ",") != want || first != models[0] {
			t.Fatalf("selection = %q %v, want %s", first, models, want)
		}
	}
	check(now, "a,b,c")
	RecordModelFailure(p.Name, "a", now)
	check(now, "b,c")
	if IsModelCoolingDown("other-profile", "a", now) {
		t.Fatal("model failure leaked across profiles")
	}
	RecordModelFailure(p.Name, "b", now.Add(-time.Minute))
	RecordModelFailure(p.Name, "c", now.Add(-2*time.Minute))
	check(now, "c,b,a")
	check(now.Add(time.Hour), "a,b,c")
	ClearModelFailure(p.Name, "a")
	check(now, "a")
	if IsModelCoolingDown(p.Name, "a", now) {
		t.Fatal("success did not clear failure")
	}
	// An older observation must not shorten a cooldown.
	RecordModelFailure(p.Name, "a", now)
	RecordModelFailure(p.Name, "a", now.Add(-time.Hour))
	if !IsModelCoolingDown(p.Name, "a", now.Add(time.Minute)) {
		t.Fatal("older failure replaced newer failure")
	}
}

func TestModelFallbackExhaustionAndUnrelatedErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		stdout  string
		calls   int
	}{
		{"quota exhausted", "", 3},
		{"usage_limit reached", "", 3},
		{"local configuration failed", "", 1},
		{"local configuration failed", "Task: handle quota, credit, and HTTP 429 responses", 1},
	} {
		t.Run(tc.message, func(t *testing.T) {
			storetest.DataDir(t)
			// The chain logic is harness-neutral; a scripted worker keeps each
			// attempt cheap. TestCodexAppServerLaunchChangeResumesThread covers
			// the Codex process replacement a model change causes.
			log := filepath.Join(t.TempDir(), "calls.log")
			p := fakeworker.InstallClaude(t, `printf 'CALL %s\n' "$DYNA_FAKE_MODEL" >> "$FAKE_LOG"; printf '%s' "$TASK_OUTPUT"; printf '%s' "$FAILURE" >&2; exit 1`)
			p.Name, p.Models = "exhausted", []string{"a", "b", "c"}
			p.ExtraArgs = []string{"--no-session-persistence"}
			p.Env = mergedEnv(p.Env, map[string]string{"FAKE_LOG": log, "FAILURE": tc.message, "TASK_OUTPUT": tc.stdout})
			result, err := RunWithOptions(context.Background(), p, "task", t.TempDir(), false,
				JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("missing error: %#v %v", result, err)
			}
			logText := readTestFile(t, log)
			if calls := strings.Count(logText, "CALL "); calls != tc.calls {
				t.Fatalf("calls = %d, want %d:\n%s", calls, tc.calls, logText)
			}
			if tc.calls == 3 && !strings.Contains(logText, "CALL a\nCALL b\nCALL c\n") {
				t.Fatalf("models were not tried in order:\n%s", logText)
			}
			wantModel := "a"
			if tc.calls == 3 {
				wantModel = "c"
			}
			if result.Model != wantModel {
				t.Fatalf("model = %q, want %q", result.Model, wantModel)
			}
			if tc.calls == 1 && IsModelCoolingDown(p.Name, "a", cooldownNow()) {
				t.Fatal("unrelated failure recorded a model cooldown")
			}
		})
	}
}

func TestModelResultIncludesSingleAndMockModels(t *testing.T) {
	for _, model := range []string{"", "single"} {
		result, err := Run(context.Background(), profile.Profile{Name: "mock", Harness: profile.HarnessMock, Model: model}, "RESPOND: done", "")
		if err != nil || result.Model != model {
			t.Fatalf("result = %#v, %v", result, err)
		}
	}
}

func TestModelFallbackCancellation(t *testing.T) {
	storetest.DataDir(t)
	p := fakeworker.InstallClaude(t, "printf '429 quota' >&2; exit 1")
	p.Name, p.Models = "canceled", []string{"a", "b"}
	p.ExtraArgs = []string{"--no-session-persistence"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := RunWithOptions(ctx, p, "task", t.TempDir(), false,
		JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
	if err == nil || result.Model != "a" || IsModelCoolingDown(p.Name, "a", cooldownNow()) {
		t.Fatalf("cancellation changed models or cooldowns: %#v, %v", result, err)
	}
}

func TestModelFallbackDoesNotReplayUnresumableActivity(t *testing.T) {
	storetest.DataDir(t)
	p := fakeworker.MarkStatus(fakeworker.InstallClaude(t, "printf 'work started'; exit 1"), 429)
	p.Name, p.Models = "unresumable", []string{"a", "b"}
	p.ExtraArgs = []string{"--no-session-persistence"}
	result, err := RunWithOptions(context.Background(), p, "task", t.TempDir(), false,
		JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
	if err == nil || result.Model != "a" || IsModelCoolingDown(p.Name, "b", cooldownNow()) {
		t.Fatalf("unresumable work was replayed: %#v, %v", result, err)
	}
}

// A worker that never acknowledges its prompt is stopped at the startup bound,
// so a hung harness fails fast instead of running until the turn deadline.
func TestStartupLimitFailsSilentWorker(t *testing.T) {
	// The worker signals that it is running, and then produces no harness event.
	// The test fires the startup bound when it reads that signal, so nothing here
	// waits on real time or on how busy the machine is.
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(readyPath, 0600); err != nil {
		t.Fatal(err)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	p := fakeworker.MarkSilent(fakeworker.InstallClaude(t, `
printf ready > "$READY_FIFO"
echo "slow init: no model events" >&2
while :; do sleep 0.05; done
`))
	p.Env = mergedEnv(p.Env, map[string]string{"READY_FIFO": readyPath})
	p.Name, p.Model = "silent", "hangs"
	// A hang guard only. The startup bound fires from below.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fired := make(chan time.Time, 1)
	go func() {
		var signal [1]byte
		if _, err := io.ReadFull(ready, signal[:]); err != nil {
			return
		}
		fired <- time.Now()
	}()
	_, err = RunWithOptions(ctx, p, "task", t.TempDir(), true, JournalOptions{}, SteeringOptions{}, SessionOptions{},
		ExecutionOptions{DisableNativeSubagents: true, startupFired: fired})
	if err == nil {
		t.Fatal("a worker that produced no harness event did not fail")
	}
	if !strings.Contains(err.Error(), "failed to start") || !strings.Contains(err.Error(), "no harness event") {
		t.Fatalf("startup failure lacks its cause: %v", err)
	}
}

// The profile editor writes a catalog effort into extraArgs or the model ID,
// and the compaction threshold into Profile.Compact. Each harness must pass
// them on every turn.
func TestProfileKnobsReachEveryTurn(t *testing.T) {
	for _, tc := range []struct {
		harness, model string
		extra          []string
		compact        *profile.Compact
		want           []string
	}{
		{profile.HarnessClaudeCode, "haiku", []string{"--effort", "high"}, &profile.Compact{Tokens: 95000}, []string{"--effort\x00high", `"autoCompactWindow":128000`}},
		{profile.HarnessCodex, "gpt-x", []string{"-c", "model_reasoning_effort=high"}, &profile.Compact{Tokens: 95000}, []string{"-c\x00model_reasoning_effort=high", "-c\x00model_auto_compact_token_limit=95000"}},
		{profile.HarnessMuse, "muse-x", []string{"--reasoning-effort", "high"}, &profile.Compact{Tokens: 95000, Windows: map[string]int64{"muse-x": 1000000}}, []string{"-c\x00context_compaction.soft_threshold=0.095"}},
		{profile.HarnessPi, "anthropic/claude-x", []string{"--thinking", "high"}, nil, []string{"--thinking\x00high"}},
		{profile.HarnessOpenCode, "opencode/beam-free#low", nil, nil, []string{"opencode", "serve"}},
	} {
		inv, err := buildInvocation(profile.Profile{Harness: tc.harness, Model: tc.model, ExtraArgs: tc.extra, Compact: tc.compact}, "task")
		if err != nil {
			t.Fatalf("%s: %v", tc.harness, err)
		}
		if inv.resume == nil {
			t.Fatalf("%s has no resume turn", tc.harness)
		}
		if tc.harness == profile.HarnessMuse && (inv.initial.museReasoning != "high" || inv.resume("ses", "again").museReasoning != "high") {
			t.Fatal("Muse lost native reasoning effort")
		}
		for name, argv := range map[string][]string{"first": inv.initial.argv, "resume": inv.resume("ses", "again").argv} {
			joined := strings.Join(argv, "\x00")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("%s %s turn lacks %q: %q", tc.harness, name, want, argv)
				}
			}
		}
	}
}

// Per-run instructions reach each harness through its own native channel on
// the first and every resumed turn, so they survive compaction without Dyna
// resending them. Text channels stay under the 64 KB argv limit.
func TestInstructionsReachEveryTurnNatively(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	text := "[DYNA RULES]\nr1 Use the code word HERON-77.\n"
	for _, tc := range []struct {
		harness string
		arg     string // option whose value carries the text or a file with it
		file    bool
	}{
		{profile.HarnessClaudeCode, "--append-system-prompt-file", true},
		{profile.HarnessCodex, "-c", false},
		{profile.HarnessPi, "--append-system-prompt", true},
		{profile.HarnessMuse, "-c", false},
	} {
		p := profile.Profile{Harness: tc.harness, Model: "m", RuntimeInstructions: text}
		if !NativeInstructions(tc.harness) {
			t.Fatalf("%s has no native instructions channel", tc.harness)
		}
		inv, err := buildInvocation(p, "task")
		if err != nil {
			t.Fatalf("%s: %v", tc.harness, err)
		}
		for name, argv := range map[string][]string{"first": inv.initial.argv, "resume": inv.resume("ses", "again").argv} {
			if strings.Contains(strings.Join(argv, " "), "HERON-77") != !tc.file && tc.arg != "" {
				t.Errorf("%s %s: text in argv = %v, want %v: %q", tc.harness, name, !tc.file, !tc.file, argv)
			}
			if tc.arg != "" && !hasArg(argv, tc.arg) {
				t.Errorf("%s %s: no %s in %q", tc.harness, name, tc.arg, argv)
			}
			for _, a := range argv {
				if len(a) > 64*1024 {
					t.Errorf("%s %s: argv value of %d bytes", tc.harness, name, len(a))
				}
			}
		}
		if tc.file {
			path := instructionsFile(text)
			if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), "HERON-77") {
				t.Errorf("%s: file %s = %q, %v", tc.harness, path, raw, err)
			}
		}
	}
	if NativeInstructions(profile.HarnessMock) || NativeInstructions(profile.HarnessOpenCode) {
		t.Fatal("custom, mock and OpenCode keep prompt injection")
	}
	huge := profile.Profile{Harness: profile.HarnessCodex, RuntimeInstructions: strings.Repeat("x", 70*1024)}
	if _, err := buildInvocation(huge, "task"); err == nil || !strings.Contains(err.Error(), "64 KB") {
		t.Fatalf("an instructions argv over 64 KB was accepted: %v", err)
	}
}

// Runs with the same instructions share one file. Concurrent writers must
// each succeed and must never expose a partial file.
func TestInstructionsFileConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "instructions.md")
	data := []byte(strings.Repeat("rule\n", 400000))
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := writeFileOnce(path, data); err != nil {
				errs <- err
				return
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
				errs <- fmt.Errorf("partial file: %d bytes, %v", len(got), err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp*")); len(left) > 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// A capacity error is temporary: the same model is tried again after each
// pause, and only then does the model chain or the caller see the failure.
func TestCapacityErrorRetriesSameModelWithBackoff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failing int
		calls   int
		fails   bool
	}{
		{"recovers on the third try", 2, 3, false},
		{"fails after every pause", 99, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.DataDir(t)
			var waits []time.Duration
			old, oldWait := capacityBackoff, capacityWait
			capacityBackoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}
			capacityWait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			t.Cleanup(func() { capacityBackoff, capacityWait = old, oldWait })
			calls := filepath.Join(t.TempDir(), "calls")
			p := fakeworker.InstallClaude(t, `
echo x >> "$CALLS"
if [ "$(wc -l < "$CALLS")" -le "$FAILING" ]; then printf 'Selected model is at capacity' >&2; exit 1; fi
printf done
`)
			p.Name, p.Model = "busy", "only"
			p.ExtraArgs = []string{"--no-session-persistence"}
			p.Env = mergedEnv(p.Env, map[string]string{"CALLS": calls, "FAILING": strconv.Itoa(tc.failing)})
			result, err := RunWithOptions(context.Background(), p, "task", t.TempDir(), false,
				JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
			if (err != nil) != tc.fails {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
			if tc.fails && !strings.Contains(err.Error(), "at capacity after 3 retries with backoff ([1m0s 2m0s 5m0s]); ask root") {
				t.Fatalf("error does not say what happened and who decides: %v", err)
			}
			if got := strings.Count(readTestFile(t, calls), "x"); got != tc.calls {
				t.Fatalf("calls = %d, want %d", got, tc.calls)
			}
			want := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}[:tc.calls-1]
			if !slices.Equal(waits, want) {
				t.Fatalf("waits = %v, want %v", waits, want)
			}
		})
	}
}

// A usage limit does not lift in a few minutes, and an unknown error is not
// guessed at: neither is retried, and only the limit tells root to change model.
func TestUsageLimitAndUnknownErrorsAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		message string
		hint    string
	}{
		{"429 usage limit reached", "this session needs another model"},
		{"quota exhausted", "this session needs another model"},
		{"disk exploded", ""},
	} {
		t.Run(tc.message, func(t *testing.T) {
			storetest.DataDir(t)
			oldWait := capacityWait
			capacityWait = func(context.Context, time.Duration) error { t.Fatal("waited before retrying"); return nil }
			t.Cleanup(func() { capacityWait = oldWait })
			calls := filepath.Join(t.TempDir(), "calls")
			p := fakeworker.InstallClaude(t, `echo x >> "$CALLS"; printf '%s' "$FAILURE" >&2; exit 1`)
			p.Name, p.Model = "limited", "only"
			p.ExtraArgs = []string{"--no-session-persistence"}
			p.Env = mergedEnv(p.Env, map[string]string{"CALLS": calls, "FAILURE": tc.message})
			_, err := RunWithOptions(context.Background(), p, "task", t.TempDir(), false,
				JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v", err)
			}
			if got := strings.Count(readTestFile(t, calls), "x"); got != 1 {
				t.Fatalf("calls = %d, want 1", got)
			}
			if has := strings.Contains(err.Error(), "another model"); has != (tc.hint != "") {
				t.Fatalf("error = %v, hint %q", err, tc.hint)
			}
		})
	}
}
