package engine

import (
	"net"
	"testing"

	"dyna-agent/internal/callerid"
)

func TestCoordinatorSocketHintRequiresVerifiedCoordinator(t *testing.T) {
	for _, id := range []callerid.Identity{
		{Kind: callerid.Worker, Run: "wf_hint"},
		{Kind: callerid.Worker, Run: "wf_hint", Agent: 1},
		{Kind: callerid.Worker, Run: "wf_hint", Session: "ses_claim"},
		{Kind: callerid.Owner},
		{Kind: callerid.Refused, Run: "wf_hint", Reason: "unverified"},
	} {
		calls := 0
		s := &socketCallers{resolve: func(net.Conn) callerid.Identity { return id }, coordinatorChanged: func(run string) {
			if run != "wf_hint" {
				t.Fatalf("hint run=%q", run)
			}
			calls++
		}}
		if _, err := s.identify(nil); (err != nil) != (id.Kind == callerid.Refused) {
			t.Fatalf("identify(%+v): %v", id, err)
		}
		want := 0
		if id.Kind == callerid.Worker && id.Agent == 0 && id.Session == "" {
			want = 1
		}
		if calls != want {
			t.Fatalf("caller %+v queued %d hints, want %d", id, calls, want)
		}
	}
}
