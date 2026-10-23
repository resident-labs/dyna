package watch

import (
	"strings"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestEngineMergeWakesAuthorOnceAndKeepsCleanupQuiet(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	key := "github.com/owner/repo#1"
	r, err := e.s.Store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	e.s.Notify.Sessions.Store = &subagent.Store{Root: t.TempDir()}
	recordtest.Write(t, e.s.Notify.Sessions.Store.Root, e.author.ParentSession, subagent.Meta{
		ID: e.author.ParentSession, Name: "lead", Room: "merge-work", RoomID: r.RoomID,
		Resumable: true, HarnessSession: "lead",
	})
	recordtest.Write(t, e.s.Notify.Sessions.Store.Root, r.Author, subagent.Meta{
		ID: r.Author, Name: "builder", Room: "merge-work", RoomID: r.RoomID,
		ParentSession: e.author.ParentSession,
		Resumable:     true, HarnessSession: "builder",
	})
	e.s.Deliver = nil
	merged, err := e.s.publishMerge(t.Context(), r, nil)
	if err != nil || !merged {
		t.Fatalf("engine publication: merged=%v err=%v", merged, err)
	}
	if err := e.s.flush(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	// Another observation with a different head still cannot queue another
	// completion notice for this PR.
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[key]
		live.Head = strings.Repeat("f", 40)
		live.Watching = true
		endRecord(live, true, e.clock)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.flush(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	posts, err := e.s.Notify.Rooms.Messages(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	mergedCount, cleanupCount := 0, 0
	for _, post := range posts {
		if strings.Contains(post.Text, "PR #1 merged.") {
			mergedCount++
			if !post.Wake || len(post.To) != 1 || post.To[0] != "merge-work:builder" {
				t.Fatalf("completion did not wake author: %+v", post)
			}
		}
		if strings.Contains(post.Text, "Your PR branch worktree") {
			cleanupCount++
			if post.Wake {
				t.Fatalf("cleanup woke author: %+v", post)
			}
		}
	}
	if mergedCount != 1 || cleanupCount != 1 {
		t.Fatalf("merged=%d cleanup=%d posts=%+v", mergedCount, cleanupCount, posts)
	}
	saved, err := state.Read(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range saved.Records[key].Notices {
		if n.Kind == "merged" && !n.Done {
			t.Fatalf("completion receipt was not persisted: %+v", n)
		}
	}
}
