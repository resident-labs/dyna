package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// startRunner starts a process whose command line is dyna's with args.
func startRunner(t *testing.T, args ...string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Args = append([]string{os.Args[0]}, args...)
	cmd.Env = append(os.Environ(), runnerEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// Startup trusts only launch records, even when a saved PID matches a runner.
func TestEngineStartupUsesOnlyCommittedLaunchRecords(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			store := &subagent.Store{Root: filepath.Join(data, "sessions")}
			id := "ses_20261020-000000-0000live"
			runner := startRunner(t, "subagent", "_turn", id)
			recordtest.Write(t, store.Root, id, subagent.Meta{ID: id, Name: "live", RoomID: "room", Room: "room", Profile: "mock", Turns: 1, Pid: runner})
			callers, err := identifyCallers(context.Background(), data, readOnly, store)
			if err != nil {
				t.Fatal(err)
			}
			if got := callers.resolver.Resolve(runner); got.Kind == callerid.Session {
				t.Errorf("unrecorded runner acquired identity: %+v", got)
			}
			cmd := exec.Command(os.Args[0])
			cmd.Args = []string{os.Args[0], "subagent", "_turn", id, "2"}
			cmd.Env = append(os.Environ(), runnerEnv+"=1")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := callerid.Start(context.Background(), cmd, callerid.Launch{DataDir: data, Name: id, Actor: callerid.Identity{Kind: callerid.Session, Session: id}, Launcher: "turn"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
			if got := callers.resolver.Resolve(cmd.Process.Pid); got.Kind != callerid.Session || got.Session != id {
				t.Errorf("committed launch lost identity: %+v", got)
			}
		})
	}
}

// An idle session has no live runner at engine start, so nothing is
// backfilled for it. Its next turn is recorded by the launcher before its
// first call, and the running engine's resolver sees that record.
func TestIdleSessionNextTurnIsRecordedAfterStart(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := &subagent.Store{Root: filepath.Join(data, "sessions")}
	idle := "ses_20261020-000000-0000idle"
	recordtest.Write(t, store.Root, idle, subagent.Meta{ID: idle, Name: "idle", RoomID: "room", Room: "room", Profile: "mock", Turns: 1})
	callers, err := identifyCallers(context.Background(), data, false, store)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Args = []string{os.Args[0], "subagent", "_turn", idle, "2"}
	cmd.Env = append(os.Environ(), runnerEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := callerid.Start(context.Background(), cmd, callerid.Launch{DataDir: data, Name: idle, Actor: callerid.Identity{Kind: callerid.Session, Session: idle}, Launcher: "turn"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	if got := callers.resolver.Resolve(cmd.Process.Pid); got.Kind != callerid.Session || got.Session != idle {
		t.Fatalf("the idle session's next turn = %+v, want %s", got, idle)
	}
}

func TestProductionCallerPolicyRejectsUnrecordedRunner(t *testing.T) {
	refused := callerid.Identity{Kind: callerid.Refused, Reason: "unrecorded runner"}
	callers := &socketCallers{
		resolve:  func(net.Conn) callerid.Identity { return refused },
		sessions: &subagent.Store{Root: filepath.Join(t.TempDir(), "sessions")},
		runRoom:  func(string) (string, error) { return "", errors.New("missing run") },
	}
	if !enforceCallers {
		t.Error("production caller enforcement is disabled")
	}
	if _, err := callers.identify(nil); err == nil {
		t.Error("resolver refusal was served")
	}
	for _, caller := range []websvc.Caller{
		{Kind: websvc.Worker, Run: "missing"},
		{Kind: websvc.Session, Session: "ses_missing"},
		{Kind: websvc.Unverified},
	} {
		if _, err := callers.actor(caller); err == nil {
			t.Errorf("caller was served: %+v", caller)
		}
	}
	if got := callers.Refused(); got != 4 {
		t.Errorf("refusals = %d; want 4", got)
	}
	if h := engineHealth("", "", time.Now(), nil, hub.New(1), callers, nil)(); h.CallersRefused != 4 {
		t.Errorf("health refusals = %d; want 4", h.CallersRefused)
	}
	var resolved notify.Claim
	_, err := postSender(websvc.Caller{Kind: websvc.Session, Session: "ses_root"}, true,
		authority.Actor{Role: authority.Root, Session: "ses_root"}, notify.Claim{Room: "requested"},
		enforceCallers, func(string, ...any) {}, func(c notify.Claim) (notify.Sender, error) { resolved = c; return notify.Sender{}, nil })
	if err != nil || resolved != (notify.Claim{Session: "ses_root", Room: "requested"}) {
		t.Errorf("root sender claim = %+v, %v", resolved, err)
	}
}

// Each call acts for its caller (requirements item 3). A session acts with
// its stored role, so the room's root seat is Root, and a paired device is
// the owner's. Missing authority and unverified callers are refused.
func TestCallsActForTheirCaller(t *testing.T) {
	store := &subagent.Store{Root: filepath.Join(t.TempDir(), "sessions")}
	agent, root, closed := "ses_20261020-000000-000agent", "ses_20261020-000000-0000root", "ses_20261020-000000-00closed"
	recordtest.Write(t, store.Root, agent, subagent.Meta{ID: agent, Name: "agent", RoomID: "room", Room: "room", Profile: "mock"})
	recordtest.Write(t, store.Root, root, subagent.Meta{ID: root, Name: "root", RoomID: "room", Room: "room", Profile: "mock", Role: subagent.RoleRoot})
	recordtest.Write(t, store.Root, closed, subagent.Meta{ID: closed, Name: "closed", RoomID: "room", Room: "room", Profile: "mock", Closed: time.Now()})
	// A worker's room comes from its run's stored row; run_lost has no row.
	runRoom := func(run string) (string, error) {
		if run == "run_lost" {
			return "", errors.New("no such run")
		}
		return "room", nil
	}
	callers := &socketCallers{sessions: store, runRoom: runRoom}
	for _, tc := range []struct {
		caller websvc.Caller
		want   authority.Actor
	}{
		{websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner}},
		{websvc.Caller{Kind: websvc.Session, Session: agent}, authority.Actor{Role: authority.Session, Session: agent, Room: "room"}},
		{websvc.Caller{Kind: websvc.Session, Session: root}, authority.Actor{Role: authority.Root, Session: root, Room: "room"}},
		{websvc.Caller{Kind: websvc.Worker, Run: "run_a"}, authority.Actor{Role: authority.Worker, Run: "run_a", Room: "room"}},
		{websvc.Caller{Kind: websvc.Worker, Run: "run_a", Agent: 2}, authority.Actor{Role: authority.Worker, Run: "run_a", Agent: 2, Room: "room"}},
	} {
		if got, err := callers.actor(tc.caller); err != nil || got != tc.want {
			t.Errorf("actor(%+v) = %+v, %v; want %+v", tc.caller, got, err, tc.want)
		}
	}
	for _, bad := range []websvc.Caller{{Kind: "admin"}, {Kind: websvc.Worker}, {Kind: websvc.Worker, Run: "run_a", Agent: -1}} {
		if _, err := callers.actor(bad); err == nil {
			t.Errorf("caller %+v was served", bad)
		}
	}
	if _, err := callers.actor(websvc.Caller{Kind: websvc.Worker, Run: "run_lost"}); err == nil {
		t.Fatal("enforcing served a worker whose run has no row")
	}
	if _, err := callers.actor(websvc.Caller{Kind: websvc.Session, Session: closed}); err == nil {
		t.Fatal("enforcing served a closed session")
	}
	// A device's call carries the owner, whatever the engine process is.
	t.Setenv(mode.EnvMode, mode.Workflow)
	calls := map[string]websvc.Call{"Who": func(ctx context.Context, _ []json.RawMessage) (any, error) { return authority.Of(ctx), nil }}
	callers.actForCallers(calls)
	if got, err := calls["Who"](context.Background(), nil); err != nil || got != (authority.Actor{Role: authority.Owner}) {
		t.Fatalf("a device call acts for %+v, %v; want the owner", got, err)
	}
}

// A workflow agent's harness process is recorded for its agent number, so
// the agent acts as itself with no claim at all: a pin, which carries no
// claim, names agent 2, even when callers are enforced.
func TestARecordedAgentActsAsItsOwnNumber(t *testing.T) {
	data := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "read line")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	agent := callerid.Identity{Kind: callerid.Worker, Run: "wf_1", Agent: 2}
	if err := callerid.RecordStarted(context.Background(), cmd.Process.Pid, callerid.Launch{DataDir: data, Name: "wf_1", Actor: agent, Launcher: "workflow"}); err != nil {
		t.Fatal(err)
	}
	store, err := callerid.OpenStore(data)
	if err != nil {
		t.Fatal(err)
	}
	r, err := callerid.New("/proc", os.Getpid(), store)
	if err != nil {
		t.Fatal(err)
	}
	callers := &socketCallers{resolve: func(net.Conn) callerid.Identity { return r.Resolve(cmd.Process.Pid) }, runRoom: func(string) (string, error) { return "room", nil }}
	caller, err := callers.identify(nil)
	if err != nil || caller != (websvc.Caller{Kind: websvc.Worker, Run: "wf_1", Agent: 2}) {
		t.Fatalf("identify = %+v, %v; want agent 2 of wf_1", caller, err)
	}
	actor, err := callers.actor(caller)
	if err != nil {
		t.Fatal(err)
	}
	if by, err := pinnedBy(caller, true, actor, true, t.Logf, nil); err != nil || by != "run wf_1 agent 2" {
		t.Fatalf("pinned by %q, %v; want run wf_1 agent 2", by, err)
	}
}
