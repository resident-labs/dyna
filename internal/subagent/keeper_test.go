package subagent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/settings"
)

func TestKeeperStoredDeadlineAndTopLevelVisibility(t *testing.T) {
	e := newTestEnv(t)
	parent := completedFixtureSession(t, e)
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, parent.ID)
	t.Setenv(mode.EnvDepth, "3")
	deadline := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	// The engine starts keepers; the stale session stamp must not make one a child.
	info, turn, err := e.m.StartForEngine(StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "SLEEP:0 RESPOND: ready", Role: "keeper", Name: "keeper", Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if info.Role != "keeper" || info.Name != "keeper" || info.Depth != 1 || info.ParentSession != "" {
		t.Fatalf("keeper must be a visible top-level session: %+v", info.Meta)
	}
	_, done := e.wait(info.ID, turn.N)
	if !done.Deadline.Equal(deadline) || done.Status != TurnOK {
		t.Fatalf("stored turn: %+v", done)
	}
	all, err := e.m.Store.ListSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range all {
		if item.ID == info.ID {
			found = item.Role == "keeper" && item.Name == "keeper"
		}
	}
	if !found {
		t.Fatal("keeper missing from session list")
	}
}

func TestKeeperDefaultDeadlineAndNormalSessionsUnlimited(t *testing.T) {
	e := newTestEnv(t)
	values := settings.Defaults()
	values.GoalKeeperTimeout = 120 * time.Millisecond
	e.m.Settings = func() settings.Values { return values }
	began := time.Now()
	keeper, queued := e.start("SLEEP:2000", func(o *StartOptions) { o.Role = "keeper" })
	latest := time.Now().Add(values.GoalKeeperTimeout)
	_, done := e.wait(keeper.ID, 1)
	if queued.Deadline.Before(began.Add(values.GoalKeeperTimeout)) || queued.Deadline.After(latest) || !done.Deadline.Equal(queued.Deadline) || done.Status != TurnStopped || !strings.Contains(done.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("keeper deadline was not enforced: %+v", done)
	}
	for _, role := range []string{"", "reviewer"} {
		info, turn := e.start("SLEEP:0", func(o *StartOptions) { o.Role = role })
		_, done := e.wait(info.ID, 1)
		if !turn.Deadline.IsZero() || !done.Deadline.IsZero() || done.Status != TurnOK {
			t.Fatalf("ordinary role %q changed: %+v", role, done)
		}
	}
	values.GoalKeeperTimeout = 0
	before := time.Now()
	fallback := e.m.turnDeadline("keeper", time.Time{})
	if fallback.Before(before.Add(5*time.Minute)) || fallback.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("fallback: %s", fallback)
	}
}

func TestKeeperSchemaRetriesShareDeadlineAndResume(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	schema := `{"type":"object","required":["n"],"properties":{"n":{"type":"number"}}}`
	deadline := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	// Pause only after the first harness attempt starts. Its invalid response
	// reaches the schema retry without relying on mock sleeps or startup speed.
	paused := make(chan error, 1)
	var once sync.Once
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" {
			once.Do(func() {
				_, err := e.m.rooms().SetSessionsPaused(e.ctx, e.room.ID, true)
				paused <- err
			})
		}
	}
	// The retry enters the existing room-hold boundary with its real context.
	// Inspect its deadline, then return a deadline error without waiting for a timer.
	// Actual timer cancellation is covered by TestKeeperDefaultDeadlineAndNormalSessionsUnlimited.
	retryDeadline := make(chan time.Time, 1)
	e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		got, _ := ctx.Deadline()
		retryDeadline <- got
		return context.DeadlineExceeded
	}
	info, _ := e.start("SLEEP:0 RESPOND: invalid", func(o *StartOptions) { o.Role, o.Schema, o.Deadline = "keeper", schema, deadline })
	_, done := e.wait(info.ID, 1)
	select {
	case err := <-paused:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("first harness attempt did not start")
	}
	select {
	case got := <-retryDeadline:
		if !got.Equal(deadline) {
			t.Fatalf("retry deadline = %s, want original %s", got, deadline)
		}
	default:
		t.Fatal("schema retry did not reach the room-hold boundary")
	}
	if done.Status != TurnError || !strings.Contains(done.Error, "deadline exceeded") || done.HarnessSession == "" || !done.Deadline.Equal(deadline) {
		t.Fatalf("retry deadline: %+v", done)
	}
	sid := done.HarnessSession
	e.m.checkpoint = nil
	e.m.TransferSleep = nil
	if _, err := e.m.rooms().SetSessionsPaused(e.ctx, e.room.ID, false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Minute).Truncate(time.Millisecond)
	wake, err := e.send(info.ID, `SLEEP:0 RESPOND: {"n":7}`, WakeOptions{Schema: schema, Deadline: deadline, RequireIdle: true})
	if err != nil {
		t.Fatal(err)
	}
	_, done = e.wait(info.ID, wake.Turn)
	if done.Status != TurnOK || done.HarnessSession != sid || !done.Deadline.Equal(deadline) {
		t.Fatalf("exact-session follow-up: %+v", done)
	}
}

func TestKeeperExecutionPermissions(t *testing.T) {
	for _, harnessName := range []string{profile.HarnessCodex, profile.HarnessClaudeCode} {
		original := profile.Profile{Name: "service", Harness: harnessName, ExtraArgs: []string{"--dangerously-bypass-approvals-and-sandbox"}}
		safe, err := KeeperProfile(original)
		if err != nil {
			t.Fatal(err)
		}
		if !safe.SafeMode || reflect.DeepEqual(safe.ExtraArgs, original.ExtraArgs) {
			t.Fatalf("unsafe profile: %+v", safe)
		}
		want := []string{
			"--permission-mode", "dontAsk", "--permission-prompts", "none",
			"--setting-sources", "", "--strict-mcp-config",
			"--tools", "Read,Glob,Grep,Bash",
			"--allowedTools", "Read,Glob,Grep,Bash(dyna room log *),Bash(dyna goal show *),Bash(dyna goal list*),Bash(dyna goal journal*),Bash(dyna room read *),Bash(dyna git pr merge-status*),Bash(dyna subagent show *),Bash(dyna git pr show *),Bash(gh pr view *),Bash(gh pr checks *),Bash(git log *),Bash(git diff *)",
			"--disallowedTools", "Edit,Write,NotebookEdit,Bash(git *--output*),Bash(git *--ext-diff*),Bash(git *--textconv*)",
		}
		if harnessName == profile.HarnessCodex {
			want = []string{"-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`}
		}
		if !reflect.DeepEqual(safe.ExtraArgs, want) || len(original.ExtraArgs) != 1 {
			t.Fatalf("permissions: %+v", safe)
		}
		if !harness.Resumable(safe) {
			t.Fatal("keeper permissions disable exact-session resume")
		}
	}
	if safe, err := KeeperProfile(profile.Profile{Harness: profile.HarnessMock}); err != nil || !safe.SafeMode {
		t.Fatal("mock keeper permissions", err)
	}
	bounded := sessionExecution("keeper")
	if !bounded.KillOnCancel || !bounded.SkipComputer || !bounded.DisableNativeSubagents || bounded.ConstrainProfile == nil || bounded.DynaShell {
		t.Fatalf("keeper execution: %+v", bounded)
	}
	for _, role := range []string{"", "reviewer"} {
		exec := sessionExecution(role)
		if exec.KillOnCancel || exec.SkipComputer || !exec.DisableNativeSubagents || exec.ConstrainProfile != nil || !exec.DynaShell {
			t.Fatalf("ordinary execution changed: %+v", exec)
		}
	}
}

func TestKeeperRequireIdleRejectsQueuedTurn(t *testing.T) {
	e := newTestEnv(t)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, turn := e.start("SLEEP:1 RESPOND: ready", func(o *StartOptions) { o.Role = "keeper" })
	defer held.lock.Close()
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.HarnessSession = "existing" }); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.Wake(e.ctx, info.ID, WakeOptions{RequireIdle: true, Deadline: time.Now().Add(time.Second)})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("queued wake: %v", err)
	}
	stored, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.N != turn.N || !stored.Deadline.Equal(turn.Deadline) {
		t.Fatalf("queued turn changed: %+v", stored)
	}
}

func TestKeeperExpiredDeadlineDoesNotStartHarness(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ready", func(o *StartOptions) { o.Role, o.Deadline = "keeper", time.Now().Add(-time.Second) })
	_, done := e.wait(info.ID, 1)
	if done.Status != TurnStopped || done.HarnessSession != "" || !strings.Contains(done.Error, "deadline exceeded") {
		t.Fatalf("expired turn: %+v", done)
	}
}

func TestKeeperStartUsesReadOnlyResumeProfile(t *testing.T) {
	p := mockProfile("service")
	p.Harness, p.ExtraArgs = profile.HarnessCodex, []string{"--sandbox", "read-only"}
	e := newTestEnv(t, p)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Role: "keeper", Profile: "service", Dir: t.TempDir(), Task: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	if !info.Resumable {
		t.Fatal("stored keeper resume capability used the original profile arguments")
	}
}

func TestKeeperRejectsUnsupportedHarnesses(t *testing.T) {
	for _, harnessName := range []string{
		profile.HarnessAntigravity, profile.HarnessOpenCode,
		profile.HarnessMuse, profile.HarnessGrok, profile.HarnessPi, profile.HarnessCustom,
		"unknown", "",
	} {
		t.Run(harnessName, func(t *testing.T) {
			_, err := KeeperProfile(profile.Profile{Name: "unsafe-profile", Harness: harnessName})
			if err == nil || !strings.Contains(err.Error(), "unsupported harness") || !strings.Contains(err.Error(), "unsafe-profile") || !strings.Contains(err.Error(), harnessName) {
				t.Fatalf("unsupported harness %q: %v", harnessName, err)
			}
		})
	}
}

func TestKeeperStartRejectsUnsupportedHarnessBeforeCreatingSession(t *testing.T) {
	p := profile.Profile{Name: "unsupported", Harness: profile.HarnessCustom, Command: []string{"false"}}
	e := newTestEnv(t, p)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	defer func() {
		if held.lock != nil {
			held.lock.Close()
		}
	}()
	_, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Inspect safely", Task: "Inspect", Profile: p.Name, Role: "keeper", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unsupported harness") {
		t.Fatalf("unsupported keeper start: %v", err)
	}
	all, err := e.m.Store.List(true)
	if err != nil || len(all) != 0 || held.lock != nil {
		t.Fatalf("unsupported keeper created a session: %+v %v", all, err)
	}
}

func TestCodexKeeperHasNoDynaMCPServer(t *testing.T) {
	generated := []string{"-c", `mcp_servers={dyna={command="/bin/dyna",args=["mcp","shell"]}}`}
	safe, err := KeeperProfile(profile.Profile{Name: "service", Harness: profile.HarnessCodex, ExtraArgs: generated})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(safe.ExtraArgs, " "), "mcp_servers") {
		t.Fatalf("keeper keeps an MCP server definition: %v", safe.ExtraArgs)
	}
}
