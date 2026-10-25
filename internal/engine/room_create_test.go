package engine

import (
	"os/exec"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/websvc"
)

// The app's New room names its creator by the same rule as dyna room
// create, never from the engine's own environment.
func TestAppCreateRoomCreatorIsNotTheEngineEnvironment(t *testing.T) {
	s := testServices(t, false)
	t.Setenv(runstore.SessionEnv, "ses_engine_env")
	repo := filepath.Join(t.TempDir(), "app-room")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// A call with no local caller is a paired device: the owner.
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	made, err := s.createRoom(ctx, roomsvc.CreateRoomInput{Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(made.ID)
	if err != nil || rec.CreatedBy != "" {
		t.Fatalf("created by %q (err %v), want the owner's empty claim", rec.CreatedBy, err)
	}
}

// A room record names its creator from the caller (requirement 3). Only the
// owner keeps what the CLI claimed, its own harness session. A caller the
// engine could not verify is never the owner.
func TestRoomCreatorComesFromTheCaller(t *testing.T) {
	alice := websvc.Caller{Kind: websvc.Session, Session: "ses_alice"}
	worker := websvc.Caller{Kind: websvc.Worker, Run: "wf_1", Agent: 2}
	unverifiedCaller := websvc.Caller{Kind: websvc.Unverified}
	owner := authority.Actor{Role: authority.Owner}
	for _, tc := range []struct {
		name    string
		caller  websvc.Caller
		local   bool
		actor   authority.Actor
		claimed string
		enforce bool
		want    string // the recorded creator, or "error"
		logged  bool
	}{
		{"the owner keeps its harness session", websvc.Caller{Kind: websvc.Owner}, true, owner, "pi:thread-1", false, "pi:thread-1", false},
		{"a paired device keeps its claim", websvc.Caller{}, false, owner, "", false, "", false},
		{"a session is its ID, whatever it claims", alice, true, authority.Actor{Role: authority.Root, Session: "ses_alice"}, "ses_bob", false, "ses_alice", false},
		{"a workflow worker is its run and agent", worker, true, authority.Actor{Role: authority.Worker, Run: "wf_1", Agent: 2}, "ses_parent", false, "run wf_1 agent 2", false},
		{"report-only: an unverified caller is unverified, not its claim", unverifiedCaller, true, owner, "ses_bob", false, "unverified", true},
		{"enforced: an unverified caller is refused", unverifiedCaller, true, authority.Actor{}, "ses_bob", true, "error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := false
			got, err := createdBy(tc.caller, tc.local, tc.actor, tc.claimed, tc.enforce, func(string, ...any) { logged = true })
			if err != nil {
				got = "error"
			}
			if got != tc.want || logged != tc.logged {
				t.Fatalf("created by %q (err %v, logged %v), want %q (logged %v)", got, err, logged, tc.want, tc.logged)
			}
		})
	}
}
