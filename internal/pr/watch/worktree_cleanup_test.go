package watch

import (
	"context"
	"testing"
)

func TestMergedPRRunsBranchCleanupOnce(t *testing.T) {
	// The service stores the closed state before invoking cleanup. A repeated
	// snapshot cannot invoke it again or send the owner another notice.
	store := &Store{Root: t.TempDir()}
	err := store.update(func(st *State) error {
		st.Records["github.com/o/r#1"] = &Record{Key: "github.com/o/r#1", Author: "author", Watching: true, State: "open", Seen: map[string]string{}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &Service{Store: store, CleanupMerged: func(ctx context.Context, r Record, branch string) error {
		calls++
		if branch != "feature" {
			t.Fatalf("branch=%q", branch)
		}
		return nil
	}}
	snap := Snapshot{}
	snap.Pull.State = "closed"
	snap.Pull.Merged = true
	snap.Pull.Head.Ref = "feature"
	for i := 0; i < 2; i++ {
		if err := service.process(context.Background(), "github.com/o/r#1", snap, "", "", nil, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("cleanup calls=%d, want 1", calls)
	}
}
