package watch

import (
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A failed batch pauses its room and sends root one waking notice. A restart
// from the saved checkpoint does not repeat the notice.
func TestFailedBatchWakesRootOnce(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	r := Record{Key: "change", RoomID: e.author.RoomID, Dir: e.author.Dir}
	b := state.MergeTrial{Branch: "trial/1", Head: "tested", Phase: "failed", RootReport: "TestBroken failed", Changes: []state.MergeTrialChange{{Key: r.Key}}}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{"repo": &b}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.stopMergeBatch(t.Context(), "repo", r, b, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := saved.MergeTrials["repo"]
	if !got.Notified || got.Phase != "awaiting-root" || saved.MergePauses[r.RoomID] != mergeBatchFailedReason {
		t.Fatalf("trial = %+v, pauses = %v", got, saved.MergePauses)
	}
	if len(e.sent) != 1 || !e.sent[0].NeedsRoot || !strings.Contains(e.sent[0].Text, "TestBroken failed") {
		t.Fatalf("notices = %+v", e.sent)
	}
	restarted := *e.s
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *got, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 {
		t.Fatal("restart repeated the notice")
	}
}
