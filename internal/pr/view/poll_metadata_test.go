package view_test

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestScanSkipsPollMetadataWorkAndKeepsLaterVisibleChanges(t *testing.T) {
	e := newEnv(t)
	rec := e.room("poll-metadata")
	root := e.svc.PRs.Watcher().Store.Root
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	scope := state.RateLimitScope{Host: "github.com", Account: "account", Resource: "core"}
	remaining := 2000
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"pr": {Key: "pr", Kind: "github", RoomID: rec.ID, Head: "head", State: "open", Watching: true, CI: "pending", QuotaAccount: "account"},
	}, RateLimits: map[string]state.RateLimit{scope.Key(): {Remaining: &remaining, Reset: now.Add(time.Hour), ObservedAt: now}}})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	var events []listchange.Change
	wakes := 0
	scan := func() {
		e.svc.PRs.Scan(signatures, work, &version, false, func(_ string, _ string, change *listchange.Change, wake bool) {
			if change != nil {
				events = append(events, *change)
			}
			if wake {
				wakes++
			}
		})
	}
	scan()
	events, wakes = nil, 0
	facts, inputs := view.CountProjectionFacts(t), view.CountWorkerInputJoins(t)
	rows, queries := view.CountRows(t), view.CountChangedKeyQueries(t)
	for i := range 3 {
		at := now.Add(time.Duration(i+1) * time.Minute)
		body, err := json.Marshal(at)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.UpdateRecords(t.Context(), root, []string{"pr"}, func(st *state.State) error {
			r := st.Records["pr"]
			r.Checked, r.ChecksAt, r.HeadVerifiedAt = at, at, at
			r.Polling.Next, r.Polling.FullChecked = at.Add(time.Minute), at
			if st.Responses == nil {
				st.Responses = map[string]map[string]state.CachedResponse{}
			}
			st.Responses["pr"] = map[string]state.CachedResponse{"/poll": {Body: body}}
			rate := st.RateLimits[scope.Key()]
			left := 1999 - i
			rate.ObservedAt, rate.Remaining = at, &left
			st.RateLimits[scope.Key()] = rate
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		scan()
	}
	current, err := state.Revision(root)
	if err != nil {
		t.Fatal(err)
	}
	if *facts != 0 || *inputs != 0 || *rows != 0 || *queries != 0 || len(events) != 0 || wakes != 0 || version != current {
		t.Fatalf("poll-only scans repeated work: facts=%d inputs=%d rows=%d queries=%d events=%d wakes=%d version=%q want=%q", *facts, *inputs, *rows, *queries, len(events), wakes, version, current)
	}
	if err := state.UpdateRecords(t.Context(), root, []string{"pr"}, func(st *state.State) error {
		st.Records["pr"].Title, st.Records["pr"].CI = "visible change", "passed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scan()
	if *facts != 1 || *inputs != 1 || *rows != 1 || *queries != 1 || len(events) != 2 || wakes != 1 {
		t.Fatalf("visible change lost or repeated: facts=%d inputs=%d rows=%d queries=%d events=%d wakes=%d", *facts, *inputs, *rows, *queries, len(events), wakes)
	}
	detail, err := e.svc.PRs.ChangeDetail(rec.ID, "pr")
	if err != nil || detail.Title != "visible change" || !detail.Checked.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("current projection lost content or poll time: %+v, %v", detail, err)
	}
}

func TestPollProjectionStillInvalidatesQuotaRecoveryAndRoomPause(t *testing.T) {
	e := newEnv(t)
	rec := e.room("poll-recovery")
	root := e.svc.PRs.Watcher().Store.Root
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	scope := state.RateLimitScope{Host: "github.com", Account: "account", Resource: "core"}
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"pr": {Key: "pr", Kind: "github", RoomID: rec.ID, Head: "head", State: "open", Watching: true, CI: "pending", QuotaAccount: "account"},
	}, RateLimits: map[string]state.RateLimit{scope.Key(): {PauseUntil: now.Add(time.Hour), ObservedAt: now}}})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	scan := func() {
		e.svc.PRs.Scan(signatures, work, &version, false, func(string, string, *listchange.Change, bool) {})
	}
	scan()
	facts := view.CountProjectionFacts(t)
	if err := state.UpdateRecords(t.Context(), root, nil, func(st *state.State) error {
		rate := st.RateLimits[scope.Key()]
		rate.PauseUntil = time.Time{}
		st.RateLimits[scope.Key()] = rate
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scan()
	if *facts != 1 {
		t.Fatalf("quota recovery reused old facts: %d observations", *facts)
	}
	if _, err := e.svc.Notify.Rooms.SetSessionsPaused(e.ctx, rec.ID, true); err != nil {
		t.Fatal(err)
	}
	scan()
	if *facts != 2 {
		t.Fatalf("room pause reused old facts: %d observations", *facts)
	}
	detail, err := e.svc.PRs.ChangeDetail(rec.ID, "pr")
	if err != nil || detail.ReviewState != "blocked" {
		t.Fatalf("pause not visible: %+v, %v", detail, err)
	}
}
