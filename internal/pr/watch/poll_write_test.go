package watch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

func TestPollWritesAvoidUnrelatedRecordCopies(t *testing.T) {
	e := newEnv(t)
	tracked := e.track()
	if err := e.s.Store.update(func(st *State) error {
		for i := range 20 {
			key := fmt.Sprintf("history-%d", i)
			st.Records[key] = &Record{Key: key, State: "closed", RoomID: tracked.RoomID}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	copies := 0
	update := updateStore
	updateStore = func(ctx context.Context, root string, fn func(*State) error) error {
		return update(ctx, root, func(st *State) error {
			copies += len(st.Records)
			return fn(st)
		})
	}
	t.Cleanup(func() { updateStore = update })
	for _, phase := range []string{"budget requests", "poll responses", "process"} {
		t.Run(phase, func(t *testing.T) {
			snapshot, err := state.ReadSnapshot(e.s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			st := snapshot.Copy()
			r := *st.Records[tracked.Key]
			gh := e.s.github(r.RoomID)
			gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{
				"github.com/cache/" + phase: {Body: []byte(`{"changed":true}`)},
			}, rates: cloneRates(st.RateLimits), now: e.s.now}
			result := pollResult{key: r.Key, gh: gh, snap: Snapshot{Pull: e.f.Pull}, unchanged: true, polledAt: e.clock}
			copies = 0
			switch phase {
			case "budget requests":
				err = e.s.saveBudgetRequests(result, r, EffectivePolicy(st, r), snapshot)
			case "poll responses":
				err = e.s.finishPoll(t.Context(), st, result)
			case "process":
				err = e.s.process(t.Context(), r.Key, result.snap, "passed", "fresh CI", nil, nil, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if copies != 0 {
				t.Fatalf("%s copied %d records through the generic writer; want only the scoped target", phase, copies)
			}
			after, err := state.ReadSnapshot(e.s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			live, ok := after.Record(r.Key)
			if !ok || !after.HasRecord("history-19") {
				t.Fatal("target update removed unrelated history")
			}
			if phase == "process" {
				if live.CI != "passed" || live.CIKey != "fresh CI" {
					t.Fatalf("process did not commit: %+v", live)
				}
			} else if len(after.Responses(r.Key)["github.com/cache/"+phase].Body) == 0 {
				t.Fatal("response update did not commit")
			}
			old, _ := snapshot.Record(r.Key)
			if old.CI != r.CI || snapshot.Responses(r.Key)["github.com/cache/"+phase].Body != nil {
				t.Fatal("target update changed the retained snapshot")
			}
		})
	}
}

func TestPollWriteKeepsCrossRecordQuotaScheduling(t *testing.T) {
	for _, mode := range []string{"recovery", "reserve"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			account := "same-account"
			scope := RateLimitScope{Host: r.Host, Account: account, Resource: "core"}
			remaining := 2000
			rate := RateLimit{Remaining: &remaining, Reset: e.clock.Add(time.Hour), ObservedAt: e.clock}
			if err := e.s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				live.QuotaAccount = account
				other := *live
				other.Key, other.Number = "github.com/owner/repo#18", 18
				other.Checked = e.clock
				other.Polling.FullChecked = e.clock
				other.Polling.Next = e.clock.Add(time.Hour)
				st.Records[other.Key] = &other
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			observed, err := state.ReadSnapshot(e.s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r, _ = observed.Record(r.Key)
			// The observation is now stale. The scope decision must use the
			// rate state committed after it, while holding the PR lock.
			if err := e.s.Store.updateRecords(nil, func(st *State) error {
				previous := rate
				if mode == "recovery" {
					previous.PauseUntil = e.clock.Add(-time.Second)
				}
				st.RateLimits[scope.Key()] = previous
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "reserve" {
				low := 100
				rate.Remaining = &low
			}
			rate.ObservedAt = e.clock.Add(time.Second)
			gh := e.s.github(r.RoomID)
			gh.requests = &requestCache{used: map[string]cachedResponse{}, rates: map[string]RateLimit{scope.Key(): rate}, now: e.s.now}
			if err := e.s.saveBudgetRequests(pollResult{key: r.Key, gh: gh}, r, EffectivePolicy(observed.Metadata(), r), observed); err != nil {
				t.Fatal(err)
			}
			current, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			other := current.Records["github.com/owner/repo#18"]
			if mode == "recovery" {
				if !other.Polling.FullChecked.IsZero() || !other.Polling.Next.IsZero() {
					t.Fatalf("recovery missed the other record: %+v", other.Polling)
				}
			} else if other.Polling.IntervalMinutes != 5 || other.Polling.Next.Before(other.Checked.Add(5*time.Minute)) {
				t.Fatalf("reserve missed the other record: %+v", other.Polling)
			}
		})
	}
}

func TestPollCannotUndoConcurrentDiscussionInvalidation(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	path := "github.com/repos/owner/repo/pulls/17/reviews?per_page=100"
	if err := e.s.Store.update(func(st *State) error {
		st.Responses = map[string]map[string]cachedResponse{}
		st.Responses[r.Key] = map[string]cachedResponse{path: {ETag: `"v1"`, Body: []byte(`[]`)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	gh := e.s.github(r.RoomID)
	gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now}
	result := pollResult{key: r.Key, gh: gh, unchanged: true, polledAt: e.clock}
	// A head refresh invalidates while this old poll remains in flight.
	if err := e.s.Store.update(func(current *State) error {
		current.Records[r.Key].Polling.DiscussionGeneration++
		response := current.Responses[r.Key][path]
		response.Stale = true
		current.Responses[r.Key][path] = response
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.finishPoll(context.Background(), st, result); err != nil {
		t.Fatal(err)
	}
	after, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !after.Responses[r.Key][path].Stale {
		t.Fatal("old poll cleared concurrent invalidation")
	}
}

func TestOtherPollWritersKeepDiscussionInvalidation(t *testing.T) {
	for _, writer := range []string{"merge", "budget"} {
		t.Run(writer, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			path := "github.com/repos/owner/repo/pulls/17/reviews?per_page=100"
			old := cachedResponse{ETag: `"v1"`, Body: []byte(`[]`)}
			if err := e.s.Store.update(func(st *State) error {
				st.Responses = map[string]map[string]cachedResponse{r.Key: {path: old}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			observed, err := state.ReadSnapshot(e.s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r, _ = observed.Record(r.Key)
			gh := e.s.github(r.RoomID)
			gh.requests = &requestCache{used: map[string]cachedResponse{path: old, "github.com/budget": {Body: []byte(`[]`)}}, rates: cloneRates(observed.Metadata().RateLimits), now: e.s.now}
			if err := e.s.Store.update(func(st *State) error {
				st.Records[r.Key].Polling.DiscussionGeneration++
				v := st.Responses[r.Key][path]
				v.Stale = true
				st.Responses[r.Key][path] = v
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if writer == "merge" {
				err = e.s.savePollResponses(r.Key, gh)
			} else {
				err = e.s.saveBudgetRequests(pollResult{key: r.Key, gh: gh}, r, EffectivePolicy(observed.Metadata(), r), observed)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !after.Responses[r.Key][path].Stale {
				t.Fatal("writer cleared concurrent invalidation")
			}
			if len(after.Responses[r.Key]["github.com/budget"].Body) == 0 {
				t.Fatal("writer lost unrelated response")
			}
		})
	}
}
