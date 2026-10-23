package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runsvc"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/worktrees"
)

func cliArgs(t *testing.T, values ...any) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, value := range values {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func TestCLICallsKeepExistingRoleAccess(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	args := make([]json.RawMessage, 64)
	for i := range args {
		args[i] = json.RawMessage("null")
	}
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker} {
		t.Run(string(role), func(t *testing.T) {
			actor := authority.Actor{Role: role, Session: "session", Room: "room"}
			caller := websvc.Caller{Kind: websvc.Session, Session: actor.Session}
			if role == authority.Owner {
				caller = websvc.Caller{Kind: websvc.Owner}
			}
			if role == authority.Worker {
				actor.Run = "wf_test"
				caller = websvc.Caller{Kind: websvc.Worker, Run: actor.Run}
			}
			ctx := localCallerContext(t, caller, actor)
			for _, name := range cliCalls {
				_, err := calls[name](ctx, args)
				if err == nil || !strings.Contains(err.Error(), "got 64") {
					t.Errorf("%s refused %s before argument validation: %v", name, role, err)
				}
			}
		})
	}
}

func TestCLIRoomCallsStayInCallerRoom(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	other, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []authority.Role{authority.Session, authority.Root, authority.Worker} {
		t.Run(string(role), func(t *testing.T) {
			a := authority.Actor{Role: role, Session: "session", Room: "own", Run: "wf_test"}
			caller := websvc.Caller{Kind: websvc.Session, Session: a.Session}
			if role == authority.Worker {
				caller = websvc.Caller{Kind: websvc.Worker, Run: a.Run}
			}
			ctx := localCallerContext(t, caller, a)
			cases := map[string][]json.RawMessage{
				"Unpin":       cliArgs(t, other.ID, "missing"),
				"SyncRefresh": cliArgs(t, other.ID, "/missing/repository"),
				"GitExport":   cliArgs(t, other.ID, "/missing/repository"),
			}
			if role != authority.Worker {
				cases["StartRun"] = cliArgs(t, other.ID, roomsvc.RunSource{Script: "missing.js"}, nil)
			}
			for name, args := range cases {
				_, err := calls[name](ctx, args)
				if err == nil || !strings.Contains(err.Error(), "only in its own room") {
					t.Errorf("%s did not enforce room: %v", name, err)
				}
			}
		})
	}
}

func TestStartRunUsesCLIRule(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Worker, Run: "wf_test"}, authority.Actor{Role: authority.Worker, Run: "wf_test", Room: "own"})
	_, err = calls["StartRun"](ctx, cliArgs(t, "own", roomsvc.RunSource{Script: "missing.js"}, nil))
	if err == nil || !strings.Contains(err.Error(), "use subagent mode") {
		t.Fatalf("worker passed the CLI run rule: %v", err)
	}
}

func finishedCallerRun(t *testing.T, id, roomID string) string {
	t.Helper()
	t.Setenv("DYNA_RUN_ID", id)
	t.Setenv(mode.EnvRoomID, roomID)
	t.Setenv(mode.EnvRoom, roomID)
	r, err := runstore.Create("caller-test", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Finish("ok", "", nil)
	return r.Meta.ID
}

func TestCLIRunCallsStayInCallerRoom(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	for _, role := range []authority.Role{authority.Session, authority.Root, authority.Worker, authority.Owner} {
		t.Run(string(role), func(t *testing.T) {
			a := authority.Actor{Role: role, Session: "session", Room: "own", Run: "wf_test"}
			caller := websvc.Caller{Kind: websvc.Session, Session: a.Session}
			if role == authority.Worker {
				caller = websvc.Caller{Kind: websvc.Worker, Run: a.Run}
			}
			if role == authority.Owner {
				caller = websvc.Caller{Kind: websvc.Owner}
			}
			ctx := localCallerContext(t, caller, a)
			for _, name := range []string{"CancelRun", "PauseRun", "RemoveRun", "ArchiveRun", "UnarchiveRun"} {
				for _, roomID := range []string{"own", "other"} {
					t.Run(name+"/"+roomID, func(t *testing.T) {
						id := finishedCallerRun(t, "wf_scope-"+string(role)+"-"+strings.ToLower(name)+"-"+roomID, roomID)
						args := cliArgs(t, id)
						switch name {
						case "PauseRun":
							args = cliArgs(t, id, false)
						case "RemoveRun":
							args = cliArgs(t, id, worktrees.DispositionRetain)
						case "ArchiveRun":
							args = cliArgs(t, id)
						case "UnarchiveRun":
							if _, err := runstore.ArchiveRun(id); err != nil {
								t.Fatal(err)
							}
						}
						_, err := calls[name](ctx, args)
						if roomID == "other" && role != authority.Owner {
							if err == nil || !strings.Contains(err.Error(), "only in its own room") {
								t.Errorf("%s did not enforce room: %v", name, err)
							}
							if _, err := runstore.ReadMeta(id); err != nil {
								t.Errorf("refused run lost its metadata: %v", err)
							}
						} else if name == "CancelRun" {
							// Finished runs cannot signal the test process. This status refusal
							// proves the caller reached the operation after the room check.
							if err == nil || !strings.Contains(err.Error(), "is not running") {
								t.Errorf("allowed cancel did not reach run status: %v", err)
							}
						} else if err != nil {
							t.Errorf("allowed %s failed: %v", name, err)
						}
					})
				}
			}
		})
	}
}

func TestClearRunsKeepsOtherRooms(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	own := finishedCallerRun(t, "wf_clear-own", "own")
	other := finishedCallerRun(t, "wf_clear-other", "other")
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "session"}, authority.Actor{Role: authority.Session, Session: "session", Room: "own"})
	got, err := calls["ClearRuns"](ctx, cliArgs(t, worktrees.DispositionRetain))
	if err != nil {
		t.Fatal(err)
	}
	result := got.(runsvc.Cleared)
	if result.Removed != 1 || len(result.Failures) != 0 {
		t.Errorf("room clear: %+v", result)
	}
	if _, err := runstore.ReadMeta(own); err == nil {
		t.Error("own completed run remained")
	}
	if _, err := runstore.ReadMeta(other); err != nil {
		t.Fatalf("other room run was removed: %v", err)
	}
	got, err = calls["ClearRuns"](localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner}), cliArgs(t, worktrees.DispositionRetain))
	if err != nil {
		t.Fatal(err)
	}
	if result := got.(runsvc.Cleared); result.Removed != 1 || len(result.Failures) != 0 {
		t.Errorf("owner clear: %+v", result)
	}
}

// Direct services must refuse an absent actor even for old runs without a room.
func TestRunServiceRejectsMissingCallerRoom(t *testing.T) {
	storetest.Isolate(t)
	finishedCallerRun(t, "wf_roomless", "")
	ctx := authority.WithActor(context.Background(), authority.Actor{})
	if err := runsvc.Pause(ctx, "wf_roomless", false); err == nil {
		t.Fatal("roomless caller changed a legacy run")
	}
}
