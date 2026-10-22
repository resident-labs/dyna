package engine

import (
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// A post through the engine is from its caller, not from what the posting
// process claims (requirement 3). Claims supply only the target room.
func TestPostSenderComesFromTheCaller(t *testing.T) {
	senderOf := func(c notify.Claim) (notify.Sender, error) {
		if c.Run != "" {
			return notify.Sender{Kind: notify.SenderWorker, Room: "dyna-agent", RunID: c.Run, Agent: c.Agent}, nil
		}
		if c.Session == "" {
			return notify.Sender{Kind: notify.SenderOwner, Room: "dyna-agent"}, nil
		}
		info := subagent.Info{}
		info.ID, info.Room = c.Session, "dyna-agent"
		return notify.Sender{Kind: notify.SenderSession, Room: info.Room, Session: info}, nil
	}
	alice := websvc.Caller{Kind: websvc.Session, Session: "ses_alice"}
	aliceActor := authority.Actor{Role: authority.Session, Session: "ses_alice"}
	owner := websvc.Caller{Kind: websvc.Owner}
	unverified := websvc.Caller{Kind: websvc.Unverified}
	worker := websvc.Caller{Kind: websvc.Worker, Run: "wf_1", Agent: 2}
	workerActor := authority.Actor{Role: authority.Worker, Run: "wf_1", Agent: 2}
	asWorker := notify.Claim{Run: "wf_1", Agent: 2, Room: "dyna-agent"}
	runWorker := websvc.Caller{Kind: websvc.Worker, Run: "wf_1"}
	runActor := authority.Actor{Role: authority.Worker, Run: "wf_1"}
	asBob := notify.Claim{Session: "ses_bob", Room: "dyna-agent"}
	asOwner := notify.Claim{Room: "dyna-agent"}
	for _, tc := range []struct {
		name    string
		caller  websvc.Caller
		local   bool
		actor   authority.Actor
		claim   notify.Claim
		enforce bool
		want    string // "session <id>", "worker <n> of run <id>", "owner", or "error"
		logged  string
	}{
		{"a session posts as itself", alice, true, aliceActor, notify.Claim{Session: "ses_alice", Room: "dyna-agent"}, false, "session ses_alice", ""},
		{"a session claiming another session is stored as itself", alice, true, aliceActor, asBob, false, "session ses_alice", ""},
		{"a session claiming the owner is stored as itself", alice, true, aliceActor, asOwner, false, "session ses_alice", ""},
		{"enforced: a session claiming another session is stored as itself", alice, true, aliceActor, asBob, true, "session ses_alice", ""},
		{"the owner posts as the owner", owner, true, authority.Actor{Role: authority.Owner}, asOwner, false, "owner", ""},
		{"the owner claiming a session is stored as the owner", owner, true, authority.Actor{Role: authority.Owner}, asBob, false, "owner", ""},
		{"a paired device is the owner", websvc.Caller{}, false, authority.Actor{Role: authority.Owner}, asOwner, false, "owner", ""},
		{"an unverified caller is refused", unverified, true, authority.Actor{Role: authority.Owner}, asBob, false, "error", ""},
		{"enforced: an unverified caller is refused", unverified, true, authority.Actor{}, asBob, true, "error", ""},
		{"a session the engine could not verify is refused", alice, true, authority.Actor{Role: authority.Owner}, asBob, false, "error", ""},
		{"a workflow worker posts as itself", worker, true, workerActor, asWorker, false, "worker 2 of run wf_1", ""},
		{"a worker claiming another agent is stored as itself", worker, true, workerActor, notify.Claim{Run: "wf_1", Agent: 3, Room: "dyna-agent"}, false, "worker 2 of run wf_1", ""},
		{"enforced: a worker claiming a session is stored as itself", worker, true, workerActor, asBob, true, "worker 2 of run wf_1", ""},
		{"the run itself never takes an agent number from its claim", runWorker, true, runActor, asWorker, false, "worker 0 of run wf_1", ""},
		{"a worker claiming another run is stored as its run", runWorker, true, runActor, notify.Claim{Run: "wf_2", Agent: 2, Room: "dyna-agent"}, false, "worker 0 of run wf_1", ""},
		{"enforced: a worker claiming another run is stored as its run", runWorker, true, runActor, notify.Claim{Run: "wf_2", Agent: 2, Room: "dyna-agent"}, true, "worker 0 of run wf_1", ""},
		{"a worker the engine could not verify is refused", worker, true, authority.Actor{Role: authority.Owner}, asWorker, false, "error", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs []string
			logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
			snd, err := postSender(tc.caller, tc.local, tc.actor, tc.claim, tc.enforce, logf, senderOf)
			got := "error"
			if err == nil {
				switch snd.Kind {
				case notify.SenderSession:
					got = "session " + snd.Session.ID
				case notify.SenderWorker:
					got = fmt.Sprintf("worker %d of run %s", snd.Agent, snd.RunID)
				default:
					got = "owner"
				}
				if snd.Room != "dyna-agent" {
					t.Errorf("sender room %q, want dyna-agent", snd.Room)
				}
			}
			if got != tc.want {
				t.Fatalf("sender %s (err %v), want %s", got, err, tc.want)
			}
			switch {
			case tc.logged == "" && len(logs) != 0:
				t.Fatalf("logged %q, want nothing", logs)
			case tc.logged != "" && (len(logs) != 1 || !strings.Contains(logs[0], tc.logged)):
				t.Fatalf("logged %q, want one line with %q", logs, tc.logged)
			}
		})
	}
}
