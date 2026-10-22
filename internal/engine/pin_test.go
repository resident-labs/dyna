package engine

import (
	"fmt"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// A pin records its caller (requirement 3). A caller the engine could not
// verify is never the owner: it is recorded as unverified and logged, and
// refused once callers are enforced.
func TestPinRecordsTheCaller(t *testing.T) {
	address := func(id string) (string, error) { return "dyna-agent:" + id, nil }
	alice := websvc.Caller{Kind: websvc.Session, Session: "ses_alice"}
	worker := websvc.Caller{Kind: websvc.Worker, Run: "wf_1", Agent: 2}
	unverified := websvc.Caller{Kind: websvc.Unverified}
	owner := authority.Actor{Role: authority.Owner}
	for _, tc := range []struct {
		name    string
		caller  websvc.Caller
		local   bool
		actor   authority.Actor
		enforce bool
		want    string // the recorded pinner, "" for the room owner, or "error"
		logged  bool
	}{
		{"the owner is the room", websvc.Caller{Kind: websvc.Owner}, true, owner, false, "", false},
		{"a paired device is the room", websvc.Caller{}, false, owner, false, "", false},
		{"a session is its address", alice, true, authority.Actor{Role: authority.Session, Session: "ses_alice"}, false, "dyna-agent:ses_alice", false},
		{"a workflow worker is its run and agent", worker, true, authority.Actor{Role: authority.Worker, Run: "wf_1", Agent: 2}, false, "run wf_1 agent 2", false},
		{"report-only: an unverified caller is unverified, not the owner", unverified, true, owner, false, "unverified", true},
		{"report-only: a session the engine could not verify is unverified", alice, true, owner, false, "unverified", true},
		{"enforced: an unverified caller is refused", unverified, true, authority.Actor{}, true, "error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs []string
			logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
			got, err := pinnedBy(tc.caller, tc.local, tc.actor, tc.enforce, logf, address)
			if err != nil {
				got = "error"
			}
			if got != tc.want {
				t.Fatalf("pinned by %q (err %v), want %q", got, err, tc.want)
			}
			if logged := len(logs) > 0; logged != tc.logged {
				t.Fatalf("logged %q, want logged %v", logs, tc.logged)
			}
		})
	}
}
