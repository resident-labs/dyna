package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

func TestReadSnapshotDoesNotCloneTheWholeState(t *testing.T) {
	empty, err := ReadSnapshot(newRoot(t))
	if err != nil || empty.Revision() != "missing" {
		t.Fatalf("empty snapshot revision: %s, %v", empty.Revision(), err)
	}
	root := newRoot(t)
	if err := Write(root, bigState(20)); err != nil {
		t.Fatal(err)
	}
	copies := 0
	original := copyForRead
	copyForRead = func(st State) State { copies++; return original(st) }
	t.Cleanup(func() { copyForRead = original })
	for range 5 {
		snap, err := ReadSnapshot(root)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Revision() != "1" {
			t.Fatalf("revision: %s", snap.Revision())
		}
		if _, ok := snap.Record("github.com/acme/widgets#0"); !ok {
			t.Fatal("missing record")
		}
	}
	if copies != 0 {
		t.Fatalf("unchanged snapshot reads cloned the whole state %d times", copies)
	}
	snap, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = snap.Copy()
	if copies != 1 {
		t.Fatalf("an explicit private copy made %d whole-state clones, want 1", copies)
	}
}

func TestSnapshotCopiesDoNotExposeMutableData(t *testing.T) {
	root := newRoot(t)
	st := cloneFixture(2)
	st.Records["pr"].ChangeID = "change"
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(snap.Copy())
	if err != nil {
		t.Fatal(err)
	}
	mutable := snap.Copy()
	mutable.Records["pr"].Notices[0].Text = "changed"
	mutable.Records["pr"].ReviewAssignments["head"][0] = "changed"
	mutable.Responses["pr"]["/pulls/1"] = CachedResponse{}
	mutable.Reviewers["room"] = Reviewer{}
	record, ok := snap.Record("change")
	if !ok {
		t.Fatal("change ID lookup failed")
	}
	record.Checks[0].Name = "changed"
	record.Findings[0].Replies[0].Message = "changed"
	record.ReviewHistory[0]["reviewHead"][1] = 'b'
	*record.Mergeable = true
	after, err := json.Marshal(snap.Copy())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("mutating a returned copy changed the snapshot")
	}
	if _, ok := snap.Record("missing"); ok {
		t.Fatal("unknown record found")
	}
	if err := Update(t.Context(), root, func(st *State) error {
		st.Records["pr"].Title = "new revision"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err = json.Marshal(snap.Copy())
	if err != nil || string(after) != string(before) {
		t.Fatalf("new write changed retained snapshot: %v", err)
	}
}

func TestQuotaRefreshDoesNotChangeARetainedSnapshot(t *testing.T) {
	root := newRoot(t)
	remaining := 10
	scope := RateLimitScope{Host: "github.com", Account: "alice", Resource: "core"}
	st := newState()
	st.Records["pr"] = &Record{Key: "pr", Host: "github.com", QuotaAccount: "alice", State: "open"}
	st.RateLimits[scope.Key()] = RateLimit{Remaining: &remaining}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	retained, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	// A different process changes only the quota index, leaving this record's
	// row revision unchanged, so refresh reuses its cached content.
	next := retained.Copy()
	updated := 5
	next.RateLimits[scope.Key()] = RateLimit{Remaining: &updated}
	next.Records, next.Responses = nil, nil
	body, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := appstate.For(mustOpen(t, root)).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE pr_state SET revision=revision+1, body=? WHERE id=1", body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord, _ := retained.Record("pr")
	newRecord, _ := fresh.Record("pr")
	if *oldRecord.RateLimit.Remaining != 10 || *newRecord.RateLimit.Remaining != 5 {
		t.Fatalf("old/new quotas: %d/%d", *oldRecord.RateLimit.Remaining, *newRecord.RateLimit.Remaining)
	}
	if retained.Revision() != "1" || fresh.Revision() != "2" {
		t.Fatal("snapshot revision changed")
	}
}

func TestSnapshotRevisionAndDataStayTogetherDuringUpdates(t *testing.T) {
	root := newRoot(t)
	st := bigState(1)
	st.Records["github.com/acme/widgets#0"].Title = "1"
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 2; i <= 12; i++ {
			err := Update(ctx, root, func(st *State) error {
				st.Records["github.com/acme/widgets#0"].Title = strconv.Itoa(i)
				return nil
			})
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range 24 {
			snap, err := ReadSnapshot(root)
			if err != nil {
				errors <- err
				return
			}
			record, _ := snap.Record("github.com/acme/widgets#0")
			if record.Title != snap.Revision() {
				errors <- fmt.Errorf("revision %s held title %s", snap.Revision(), record.Title)
				return
			}
		}
	}()
	close(start)
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("concurrent snapshot test did not finish")
	}
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestChangedKeysRetainsSnapshotsAndCallerIsolation(t *testing.T) {
	root := newRoot(t)
	initial := cloneFixture(2)
	initial.Records["other"] = &Record{Key: "other", State: "open", Title: "unchanged"}
	if err := Write(root, initial); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(before.ChangedKeys(Snapshot{})); got != 2 {
		t.Fatalf("initial changed keys = %d, want 2", got)
	}
	assertKeys := func(current, previous Snapshot, want ...string) {
		t.Helper()
		got := current.ChangedKeys(previous)
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("changed keys = %v, want %v", got, want)
		}
	}
	assertKeys(before, before)
	mutable := before.Copy()
	mutable.Records["pr"].Title = "changed"
	if err := Write(root, mutable); err != nil {
		t.Fatal(err)
	}
	after, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(after, before, "pr")
	// Mutating either public copy cannot alter a retained snapshot or its delta.
	mutable.Records["pr"].Findings[0].Replies[0].Message = "caller mutation"
	record, ok := after.Record("pr")
	if !ok {
		t.Fatal("missing changed record")
	}
	record.ReviewAssignments["head"][0] = "caller mutation"
	cached := after.Responses("pr")
	cached["/pulls/1"] = CachedResponse{}
	retained, _ := before.Record("pr")
	current, _ := after.Record("pr")
	if retained.Title == "changed" || current.Findings[0].Replies[0].Message == "caller mutation" || current.ReviewAssignments["head"][0] == "caller mutation" {
		t.Fatal("caller mutation changed retained snapshot data")
	}
	assertKeys(after, before, "pr")
	if err := Update(t.Context(), root, func(st *State) error {
		st.Reviewers["room"] = Reviewer{Profile: "new"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(metadata, after)
	if err := Update(t.Context(), root, func(st *State) error {
		delete(st.Records, "pr")
		delete(st.Responses, "pr")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(removed, metadata, "pr")
	assertKeys(after, before, "pr")
}

func TestChangedKeysIncludesQuotaOnlyDurableRefresh(t *testing.T) {
	root := newRoot(t)
	st := newState()
	left, right := 10, 20
	scope := RateLimitScope{Host: "github.com", Account: "alice", Resource: "core"}
	st.Records["alice"] = &Record{Key: "alice", Host: "github.com", QuotaAccount: "alice", State: "open"}
	st.Records["bob"] = &Record{Key: "bob", Host: "github.com", QuotaAccount: "bob", State: "open"}
	st.RateLimits[scope.Key()] = RateLimit{Remaining: &left}
	st.RateLimits[(RateLimitScope{Host: "github.com", Account: "bob", Resource: "core"}).Key()] = RateLimit{Remaining: &right}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata := before.Metadata()
	updated := 5
	metadata.RateLimits[scope.Key()] = RateLimit{Remaining: &updated}
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := appstate.For(mustOpen(t, root)).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE pr_state SET revision=revision+1, body=? WHERE id=1", body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	keys := after.ChangedKeys(before)
	if !reflect.DeepEqual(keys, []string{"alice"}) {
		t.Fatalf("quota-only changed keys = %v, want alice", keys)
	}
	old, _ := before.Record("alice")
	fresh, _ := after.Record("alice")
	if *old.RateLimit.Remaining != 10 || *fresh.RateLimit.Remaining != 5 {
		t.Fatal("quota refresh changed a retained snapshot")
	}
}
