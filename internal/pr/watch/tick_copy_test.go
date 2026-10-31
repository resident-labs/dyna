package watch

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

func TestTickCopiesOnlyChangedRowsAcrossPhasesAndTicks(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.write()
	r := e.track()
	if err := e.s.SetReviewEnabled(r.RoomID, false); err != nil {
		t.Fatal(err)
	}
	e.tick()
	e.tick() // Include the previous tick's delivered notice in the retained view.
	whole, rows := 0, 0
	read, copyRow := readStore, copyTickRecord
	readStore = func(root string) (State, error) { whole++; return read(root) }
	copyTickRecord = func(snapshot state.Snapshot, key string) (Record, bool) {
		rows++
		return copyRow(snapshot, key)
	}
	t.Cleanup(func() { readStore, copyTickRecord = read, copyRow })
	e.tick()
	e.tick()
	if whole != 0 || rows != 0 {
		t.Fatalf("idle ticks copied the whole store %d times and %d records; want zero", whole, rows)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].Notes.Intent = "one committed row changed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if whole != 0 || rows != 1 {
		t.Fatalf("one changed row caused %d whole-store and %d record copies; want 0 and 1", whole, rows)
	}
	if got, err := e.s.Store.Get(r.Key); err != nil || got.Notes.Intent != "one committed row changed" {
		t.Fatalf("tick lost the committed notes: %q, %v", got.Notes.Intent, err)
	}
}

func TestFinishPollKeepsWorkerResponseEntriesIsolated(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := "github.com/old", "github.com/new"
	st.Responses = map[string]map[string]cachedResponse{}
	st.Responses[r.Key] = map[string]cachedResponse{oldPath: {Body: []byte(`{}`)}}
	gh := e.s.github(r.RoomID)
	gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{newPath: {Body: []byte(`{}`)}}}
	// The routing gate leaves processing for a later pass; cache saving still runs.
	unlock, owned, err := e.s.tryPublicationGate(r.Key)
	if err != nil || !owned {
		t.Fatalf("hold publication: owned=%v, error=%v", owned, err)
	}
	defer unlock()
	result := pollResult{key: r.Key, gh: gh, snap: Snapshot{Pull: e.f.Pull}, polledAt: e.clock, unchanged: true}
	if err := e.s.finishPoll(t.Context(), st, result); err != nil {
		t.Fatal(err)
	}
	if _, found := st.Responses[r.Key][newPath]; found {
		t.Fatal("poll completion changed the worker's retained input map")
	}
	if _, found := gh.requests.entries[newPath]; found {
		t.Fatal("poll completion changed a concurrent reader's response entries")
	}
	committed, err := state.ReadSnapshot(e.s.Store.Root)
	if err != nil || len(committed.Responses(r.Key)[newPath].Body) == 0 {
		t.Fatalf("isolating the input lost the new response: %v", err)
	}
}

func TestTickReadViewKeepsRetainedRowsAndMetadataIsolated(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	first, err := e.s.tickView().read(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.updateRecords([]string{r.Key}, func(st *State) error {
		st.Records[r.Key].Notes.Intent = "committed next row"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := e.s.tickView().read(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Records[r.Key].Notes.Intent == second.Records[r.Key].Notes.Intent || second.Records[r.Key].Notes.Intent != "committed next row" {
		t.Fatal("refresh mutated the retained observation or lost the committed row")
	}
	first.Reviewers[r.RoomID] = Reviewer{Profile: "uncommitted"}
	delete(first.Records, r.Key)
	third, err := e.s.tickView().read(e.s.Store.Root)
	if err != nil || third.Records[r.Key] == nil || third.Reviewers[r.RoomID].Profile == "uncommitted" {
		t.Fatalf("a prior observation changed the next view's maps or metadata: %v", err)
	}
	mutable, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	mutable.Records[r.Key].Notes.Intent = "uncommitted caller edit"
	if third.Records[r.Key].Notes.Intent != "committed next row" {
		t.Fatal("a mutable public reader changed the private Tick row")
	}
}

func TestMergeTickReusesUnchangedReadRows(t *testing.T) {
	e := newEnv(t)
	e.write()
	e.track()
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	whole, rows := 0, 0
	read, copyRow := readStore, copyTickRecord
	readStore = func(root string) (State, error) {
		pc, _, _, _ := runtime.Caller(2)
		name := runtime.FuncForPC(pc).Name()
		if strings.HasSuffix(name, ".mergeTickLocked") || strings.HasSuffix(name, ".advanceMergeTrial") {
			whole++
		}
		return read(root)
	}
	copyTickRecord = func(snapshot state.Snapshot, key string) (Record, bool) { rows++; return copyRow(snapshot, key) }
	t.Cleanup(func() { readStore, copyTickRecord = read, copyRow })
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if whole != 0 || rows != 0 {
		t.Fatalf("unchanged merge tick copied %d whole states and %d records", whole, rows)
	}
}
