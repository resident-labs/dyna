package watch

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// A release that starts while a hold is in its GitHub call waits for the hold to
// finish. Otherwise the two gh pr ready calls could cross and leave GitHub ready
// while Dyna keeps the hold.
func TestReleaseWaitsForInFlightHoldOnSamePR(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "draft-order"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: t.TempDir()}, Notify: notify.New(rooms, subagent.NewManager())}
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) { return n.ID, nil }
	const key = "github.com/owner/repo#17"
	const url = "https://github.com/owner/repo/pull/17"
	if err := s.Store.update(func(st *State) error {
		st.Records = map[string]*Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: rm.ID, State: state.PROpen, Watching: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	inGitHub := 0
	overlap := false
	atHold := make(chan struct{})
	openHold := make(chan struct{})
	s.GH = GitHub{Executable: "gh", run: func(_ context.Context, cmd *exec.Cmd) error {
		mu.Lock()
		inGitHub++
		if inGitHub > 1 {
			overlap = true
		}
		calls = append(calls, strings.Join(cmd.Args[1:], " "))
		isHold := slices.Contains(cmd.Args, "--undo")
		mu.Unlock()
		if isHold {
			atHold <- struct{}{}
			<-openHold
		}
		mu.Lock()
		inGitHub--
		mu.Unlock()
		return nil
	}}
	ctx := context.Background()
	holdDone := make(chan error, 1)
	go func() { holdDone <- s.SetDraft(ctx, key, true, "waiting for the shared facts fix") }()
	<-atHold
	// While the hold is in its GitHub call, the PR's draft lock must be held.
	draftLocks.Lock()
	lock := draftLocks.byKey[key]
	draftLocks.Unlock()
	if lock == nil || lock.TryLock() {
		t.Fatal("hold ran its GitHub call without holding the PR's draft lock")
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- s.SetDraft(ctx, key, false, "") }()
	close(openHold)
	if err := <-holdDone; err != nil {
		t.Fatal(err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatal("hold and release ran GitHub calls at the same time")
	}
	want := []string{"pr ready --undo -- " + url, "pr ready -- " + url}
	if !slices.Equal(calls, want) {
		t.Fatalf("GitHub calls = %q, want %q", calls, want)
	}
	if saved, err := s.Store.Get(key); err != nil || saved.Hold != nil {
		t.Fatalf("release left the hold in place: %+v %v", saved.Hold, err)
	}
}
