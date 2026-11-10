package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

func TestContextRoomCI(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	snapshot := func(who contract.Principal) string {
		t.Helper()
		got, problem := r.s.ContextSnapshot(ctx, who, contract.ContextSnapshotRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, section := range got.Sections {
			if section.Key == contract.ContextRoom {
				return section.Text
			}
		}
		return ""
	}
	add := func(room contract.RoomID, repo contract.RepoName, on bool) {
		t.Helper()
		if err := r.s.store.AddRoomRepo(ctx, room, repo, false); err != nil {
			t.Fatal(err)
		}
		if err := r.s.store.SetRepoCI(ctx, repo, on, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshot(r.worker); got != "" {
		t.Fatalf("no CI: %q", got)
	}
	add(r.room, "owner/off", false)
	if err := r.s.store.CreateRoom(ctx, "other", "other", r.s.now()); err != nil {
		t.Fatal(err)
	}
	add("other", "private/hidden", true)
	// Even listing the other room's repository does not make its runners ours.
	if err := r.s.store.AddRoomRepo(ctx, r.room, "private/hidden", false); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(r.worker); got != "" {
		t.Fatalf("off or other room leaked: %q", got)
	}
	add(r.room, "owner/z", true)
	add(r.room, "owner/a", true)
	const want = "Dyna CI runners: owner/a, owner/z (dyna-ci skill)"
	for _, who := range []contract.Principal{r.root, r.worker} {
		if got := snapshot(who); got != want {
			t.Fatalf("default settings with CI: %q", got)
		}
	}
	// Fresh snapshots (the source for each new turn) notice flips without a cached setting.
	for _, repo := range []contract.RepoName{"owner/a", "owner/z"} {
		if err := r.s.store.SetRepoCI(ctx, repo, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshot(r.worker); got != "" {
		t.Fatalf("CI off: %q", got)
	}
	for i := 0; i < 35; i++ {
		add(r.room, contract.RepoName(fmt.Sprintf("owner/repository-%03d-%s", i, strings.Repeat("x", 70))), true)
	}
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, RootApproves: map[contract.RootApproval]bool{contract.RootApprovesAddons: true}}); problem != nil {
		t.Fatal(problem)
	}
	full, problem := r.s.ContextSnapshot(ctx, r.root, contract.ContextSnapshotRequest{Section: contract.ContextRoom})
	if problem != nil || len(full.Sections) != 1 || !strings.Contains(full.Sections[0].Text, "owner/repository-034-") || strings.Contains(full.Sections[0].Text, "private/hidden") {
		t.Fatalf("full room section: %+v %v", full, problem)
	}
	got := snapshot(r.root)
	if utf8.RuneCountInString(got) > roomChars || !strings.HasPrefix(got, "Dyna CI runners: ") || !strings.Contains(got, "(dyna-ci skill)") || strings.Contains(got, "private/hidden") {
		t.Fatalf("long list (%d): %q", utf8.RuneCountInString(got), got)
	}
}

// The dispatcher snapshots CI state on each new turn, without waking an idle box on a flip.
func TestContextRoomCIAtNextTurn(t *testing.T) {
	s, room, worker, ctx := setup(t)
	caller := boxCaller(worker)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetRepoCI(ctx, "owner/repo", true, 0); err != nil {
		t.Fatal(err)
	}
	ch, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	next := func() contract.TurnStart {
		t.Helper()
		select {
		case msg := <-ch:
			start, ok := msg.Body.(contract.TurnStart)
			if !ok {
				t.Fatalf("not a start: %+v", msg)
			}
			return start
		case <-time.After(time.Second):
			t.Fatal("no turn start")
		}
		return contract.TurnStart{}
	}
	start := next()
	hasCI := func(start contract.TurnStart) bool {
		for _, section := range start.Sections {
			if section.Key == contract.ContextRoom && strings.Contains(section.Text, "owner/repo (dyna-ci skill)") {
				return true
			}
		}
		return false
	}
	if !hasCI(start) {
		t.Fatalf("initial sections: %+v", start.Sections)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.SetRepoCI(ctx, "owner/repo", false, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ch:
		t.Fatalf("CI flip woke box: %+v", msg)
	case <-time.After(30 * time.Millisecond):
	}
	if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "next turn"}); problem != nil {
		t.Fatal(problem)
	}
	if start := next(); hasCI(start) {
		t.Fatalf("stale sections: %+v", start.Sections)
	}
}
