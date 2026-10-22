package state

import "testing"

func TestIssueWorkPersistsAndSnapshotsAreIndependent(t *testing.T) {
	root := newRoot(t)
	work := IssueWork{RoomID: "room-a", Repo: "github.com/acme/widgets", Number: 3, Session: "session-a"}
	key := IssueWorkKey(work.RoomID, work.Repo, work.Number)
	st := State{Version: 1, IssueWork: map[string]IssueWork{key: work}}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root)
	if err != nil || got.IssueWork[key] != work {
		t.Fatalf("work = %+v, %v", got.IssueWork, err)
	}
	delete(got.IssueWork, key)
	got, err = Read(root)
	if err != nil || got.IssueWork[key] != work {
		t.Fatalf("snapshot changed: %+v, %v", got.IssueWork, err)
	}
	if key == IssueWorkKey("room-b", work.Repo, 3) || key == IssueWorkKey(work.RoomID, "github.com/other/repo", 3) {
		t.Fatal("issue identity crosses rooms or repositories")
	}
}
