package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssuesRoundTripAndTriage(t *testing.T) {
	s, rec, _ := pinFixture(t)
	a, err := s.FileIssue(rec.ID, IssueInput{Title: "First", Note: "detail", File: "file.go", Line: 12, Filer: "alpha:agent"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.FileIssue(rec.ID, IssueInput{Title: "Second", Note: "", File: "", Line: 0, Filer: "alpha:agent"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Issues(rec.ID, false)
	if err != nil || len(rows) != 2 || rows[0].ID != b.ID || rows[1].ID != a.ID || a.Number != 1 || b.Number != 2 || rows[0].Number != 2 || rows[1].Number != 1 {
		t.Fatalf("issues: %+v %v", rows, err)
	}
	post, err := s.Message(rec.ID, a.ID)
	if err != nil || post.Kind != "issue" || post.Notice.Kind != "filed" || WakesRoot(post, rec.Name) {
		t.Fatalf("post: %+v %v", post, err)
	}
	if ev, ok := post.Issue(); !ok || ev.Number != 1 || ev.Title != "First" || ev.Detail != "detail" || ev.State != IssueStateOpen {
		t.Fatalf("filed event: %+v %v", ev, ok)
	}
	if !strings.Contains(post.Text, "alpha:agent filed issue D-1: First") {
		t.Fatalf("post text: %q", post.Text)
	}
	if _, err = s.ChangeIssue(rec.ID, a.ID, "alpha:agent", func(i *Issue) error { i.State = "closed"; i.Reason = "fixed"; return nil }); err == nil {
		t.Fatal("agent triaged issue")
	}
	if _, err = s.ChangeIssue(rec.ID, a.ID, "alpha", func(i *Issue) error { i.State = "closed"; return nil }); err == nil {
		t.Fatal("closed without reason")
	}
	got, err := s.ChangeIssue(rec.ID, "d1", "alpha:root", func(i *Issue) error { i.State = "closed"; i.Reason = "fixed"; return nil })
	if err != nil || got.ChangedBy != "alpha:root" || got.ChangedAt == 0 || got.Label() != "D-1" {
		t.Fatalf("close: %+v %v", got, err)
	}
	if _, err = s.ChangeIssue(rec.ID, a.ID, "alpha", func(i *Issue) error { i.State = "goal"; i.GoalID = "g1"; return nil }); err == nil {
		t.Fatal("triaged closed issue")
	}
	rows, _ = s.Issues(rec.ID, false)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	reopened := &Store{Root: s.Root}
	rows, err = reopened.Issues(rec.ID, true)
	if err != nil || len(rows) != 2 || rows[1].State != "closed" || rows[1].Number != 1 {
		t.Fatalf("round trip: %+v %v", rows, err)
	}
	c, err := reopened.FileIssue(rec.ID, IssueInput{Title: "Third", Note: "", File: "", Line: 0, Filer: rec.Name})
	if err != nil || c.Number != 3 {
		t.Fatalf("number after close: %+v %v", c, err)
	}
	other, err := s.Create(CreateInput{Name: "beta", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.FileIssue(other.ID, IssueInput{Title: "Separate room", Note: "", File: "", Line: 0, Filer: other.Name})
	if err != nil || first.Number != 1 {
		t.Fatalf("other room: %+v %v", first, err)
	}
}

func TestIssueTriageDoesNotBlockRoomPosts(t *testing.T) {
	s, rec, _ := pinFixture(t)
	i, err := s.FileIssue(rec.ID, IssueInput{Title: "Export", Note: "", File: "", Line: 0, Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, triaged := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.ChangeIssue(rec.ID, i.ID, rec.Name, func(i *Issue) error {
			close(entered)
			<-release
			i.State = "exported"
			i.GitHubURL = "https://github.com/a/b/issues/1"
			return nil
		})
		triaged <- err
	}()
	<-entered
	posted := make(chan error, 1)
	go func() {
		_, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Text: "Other work"})
		posted <- err
	}()
	select {
	case err := <-posted:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("triage blocked an unrelated room post")
	}
	close(release)
	if err := <-triaged; err != nil {
		t.Fatal(err)
	}
}

// Issue writes go to dyna.db only: the legacy file stays as it was, so an
// older binary can still read it after a rollback.
func TestIssueWritesLeaveTheLegacyFileUnchanged(t *testing.T) {
	s, rec, _ := pinFixture(t)
	path := filepath.Join(s.Root, rec.ID, IssuesFile)
	legacy := []byte(`[]` + "\n")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	i, err := s.FileIssue(rec.ID, IssueInput{Title: "Kept in the database", Note: "", File: "", Line: 0, Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeIssue(rec.ID, i.ID, rec.Name, func(i *Issue) error { i.State = "closed"; i.Reason = "done"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(legacy) {
		t.Fatalf("legacy issues file = %q, %v; want it unchanged", got, err)
	}
	rows, err := s.Issues(rec.ID, true)
	if err != nil || len(rows) != 1 || rows[0].State != "closed" {
		t.Fatalf("issues = %+v, %v", rows, err)
	}
}

// Three issues copied from a real room's issues.json import once, newest
// first, with every field intact.
func TestLegacyIssuesImportKeepsOrderAndFields(t *testing.T) {
	s, rec, _ := pinFixture(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "issues-real.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, rec.ID, IssuesFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var want []Issue
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i].Number = len(want) - i
	}
	got, err := s.Issues(rec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imported issues:\n got %+v\nwant %+v", got, want)
	}
}

// The room watcher compares revisions, so every write must change it.
func TestIssuesRevisionChangesOnEveryWrite(t *testing.T) {
	s, rec, _ := pinFixture(t)
	rev := func() int64 {
		t.Helper()
		n, err := s.IssuesRevision(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := rev()
	i, err := s.FileIssue(rec.ID, IssueInput{Title: "Watch", Note: "", File: "", Line: 0, Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	filed := rev()
	if _, err := s.ChangeIssue(rec.ID, i.ID, rec.Name, func(i *Issue) error { i.State = "closed"; i.Reason = "seen"; return nil }); err != nil {
		t.Fatal(err)
	}
	if changed := rev(); !(start < filed && filed < changed) {
		t.Fatalf("revisions %d, %d, %d; want each write to raise it", start, filed, changed)
	}
}

// A damaged legacy file fails the import and names the file; once it is
// repaired, the next call imports it.
func TestDamagedLegacyIssuesFailUntilRepaired(t *testing.T) {
	s, rec, _ := pinFixture(t)
	path := filepath.Join(s.Root, rec.ID, IssuesFile)
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issues(rec.ID, true); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("damaged import error = %v; want one that names %s", err, path)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "issues-real.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Issues(rec.ID, true); err != nil || len(rows) != 3 {
		t.Fatalf("issues after repair = %d, %v; want the 3 repaired issues", len(rows), err)
	}
}

// Deleting a room removes its issues, which live outside the room folder.
func TestDeletedRoomKeepsNoIssues(t *testing.T) {
	s, rec, _ := pinFixture(t)
	if _, err := s.FileIssue(rec.ID, IssueInput{Title: "Gone with the room", Note: "", File: "file.go", Line: 3, Filer: rec.Name}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Issues(rec.ID, true); err != nil || len(rows) != 0 {
		t.Fatalf("deleted room issues = %+v, %v; want none", rows, err)
	}
	if rev, err := s.IssuesRevision(rec.ID); err != nil || rev != 0 {
		t.Fatalf("deleted room revision = %d, %v; want 0", rev, err)
	}
}

// A failed issue cleanup leaves the room in place, so deletion can be retried.
func TestRoomDeletionKeepsTheRoomWhenIssueCleanupFails(t *testing.T) {
	s, rec, _ := pinFixture(t)
	other, err := s.Create(CreateInput{Name: "beta", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(s.Root, other.ID, IssuesFile)
	if err := os.WriteFile(damaged, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(rec.ID, nil); err == nil {
		t.Fatal("deletion succeeded although the issue import failed")
	}
	if _, err := s.Resolve(rec.ID); err != nil {
		t.Fatalf("room removed after a failed issue cleanup: %v", err)
	}
	if err := os.Remove(damaged); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(rec.ID, nil); err != nil {
		t.Fatalf("retried deletion: %v", err)
	}
	if _, err := s.Resolve(rec.ID); err == nil {
		t.Fatal("room still resolves after the retried deletion")
	}
}
