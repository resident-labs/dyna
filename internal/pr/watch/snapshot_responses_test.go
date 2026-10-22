package watch

import (
	"testing"
	"time"

	prstate "dyna-agent/internal/pr/state"
)

func TestPollResponsesStayImmutableDuringBudgetRequests(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	unlock, owned, err := e.s.tryPublicationGate(r.Key)
	if err != nil || !owned {
		t.Fatalf("hold existing publication gate: owned=%v, error=%v", owned, err)
	}
	defer unlock()
	gh := e.s.github(r.RoomID)
	pullPath := "github.com/owner/repo/pulls/17"
	jobsPath := "github.com/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	gh.requests = &requestCache{used: map[string]cachedResponse{pullPath: {Body: []byte("{}")}}, rates: cloneRates(st.RateLimits), now: e.s.now}
	result := pollResult{key: r.Key, gh: gh, snap: Snapshot{Pull: e.f.Pull}, started: time.Now(), polledAt: e.clock}
	if err := e.s.finishPoll(t.Context(), st, result); err != nil {
		t.Fatal(err)
	}
	snapshot, err := prstate.ReadSnapshot(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Responses(r.Key)[pullPath].Body) == 0 {
		t.Fatal("poll did not commit the original response")
	}
	gh.requests.use(jobsPath, cachedResponse{Body: []byte("{}")})
	if _, ok := snapshot.Responses(r.Key)[jobsPath]; ok {
		t.Fatal("a later worker request mutated the retained committed snapshot")
	}
	current, err := prstate.ReadSnapshot(e.s.Store.Root)
	if err != nil || current.Revision() != snapshot.Revision() {
		t.Fatalf("request map mutation changed the PR revision: %v", err)
	}
	if _, ok := current.Responses(r.Key)[jobsPath]; ok {
		t.Fatal("uncommitted budget request reached the current snapshot")
	}
}
