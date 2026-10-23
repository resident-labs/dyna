package room

import (
	"testing"
)

// filingInput is the librarian's shape: the typed note fields a smell carries.
func filingInput(filer string) IssueInput {
	return IssueInput{
		Title: "Library links cannot remove retired paths", Note: "Files: docs/a.md\nSymbols: Link.Remove\nCost: A stale link misleads.\nDirection: Remove links with the doc.",
		File: "docs/a.md", Line: 12, Filer: filer,
		Files:   []IssueFile{{Path: "docs/a.md", Line: 12}},
		Symbols: []string{"Link.Remove"}, Cost: "A stale link misleads.", Direction: "Remove links with the doc.",
	}
}

// A filed issue is one quiet typed event that carries the D-N number and the
// facts the card shows, and it wakes nobody: a new issue is backlog.
func TestFileIssuePostsOneQuietEvent(t *testing.T) {
	s, rec, _ := pinFixture(t)
	for i, filer := range []string{"alpha:librarian", "alpha:agent", "alpha:root"} {
		issue, err := s.FileIssue(rec.ID, filingInput(filer))
		if err != nil {
			t.Fatal(err)
		}
		if issue.Number != i+1 {
			t.Fatalf("issue number = %d, want %d", issue.Number, i+1)
		}
		msg, err := s.Message(rec.ID, issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != string(FamilyIssue) || msg.Notice.Kind != string(IssueFiled) || msg.Wake || msg.Deliverable() {
			t.Fatalf("filed event of %s = %+v", filer, msg)
		}
		if WakesRoot(msg, rec.Name) {
			t.Fatalf("the issue filed by %s wakes root", filer)
		}
		ev, ok := msg.Issue()
		if !ok {
			t.Fatalf("filed event of %s has no payload: %+v", filer, msg)
		}
		if ev.Number != issue.Number || ev.Number == 0 || ev.Title != issue.Title || ev.Filer != filer || ev.State != IssueStateOpen {
			t.Fatalf("filed payload of %s = %+v (issue %+v)", filer, ev, issue)
		}
		if len(ev.Files) != 1 || ev.Files[0] != (IssueFile{Path: "docs/a.md", Line: 12}) || len(ev.Symbols) != 1 || ev.Cost == "" || ev.Direction == "" {
			t.Fatalf("filed facts of %s = %+v", filer, ev)
		}
		if ev.Detail == "" {
			t.Fatalf("filed event of %s lost its note", filer)
		}
		msgs, err := s.Messages(rec.ID)
		if err != nil || len(msgs) != i+1 {
			t.Fatalf("posts after %d filings = %+v, %v", i+1, msgs, err)
		}
	}
}

// A close posts its own event, keyed by the issue, so the web folds it into the
// filing's card. It names what closed the issue and stays quiet.
func TestCloseIssuePostsTheIssueEvent(t *testing.T) {
	s, rec, _ := pinFixture(t)
	issue, err := s.FileIssue(rec.ID, filingInput("alpha:librarian"))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.ChangeIssueFrom(rec.ID, issue.ID, "alpha:root", CloseOrigin{PR: 542, Commit: "24c91eb"}, func(i *Issue) error {
		i.State = string(IssueStateClosed)
		i.Reason = "Fixed by PR #542"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("posts = %+v, %v", msgs, err)
	}
	msg := msgs[1]
	if msg.Kind != string(FamilyIssue) || msg.Notice.Kind != string(IssueClosed) || msg.Wake || msg.Deliverable() || WakesRoot(msg, rec.Name) {
		t.Fatalf("close event = %+v", msg)
	}
	ev, ok := msg.Issue()
	if !ok || ev.IssueID != issue.ID || ev.Number != issue.Number || ev.State != IssueStateClosed || ev.Actor != "alpha:root" || ev.Filer != "alpha:librarian" {
		t.Fatalf("close payload = %+v, %v", ev, ok)
	}
	if ev.ClosedBy == nil || ev.ClosedBy.PR != 542 || ev.ClosedBy.Commit != "24c91eb" || ev.Reason != "Fixed by PR #542" {
		t.Fatalf("close origin = %+v", ev.ClosedBy)
	}
	if closed.State != string(IssueStateClosed) {
		t.Fatalf("issue state = %q", closed.State)
	}
}

// An issue given to a goal holder is assigned to that session, which reads the
// quiet event beside the owner feed.
func TestIssueGoalAssignmentNamesTheAssignee(t *testing.T) {
	s, rec, _ := pinFixture(t)
	issue, err := s.FileIssue(rec.ID, filingInput("alpha:librarian"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeIssue(rec.ID, issue.ID, "alpha:root", func(i *Issue) error {
		i.State = string(IssueStateGoal)
		i.GoalID = "g7"
		i.Assignee = "alpha:librarian"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("posts = %+v, %v", msgs, err)
	}
	ev, ok := msgs[1].Issue()
	if !ok || ev.Type != IssueUpdated || ev.State != IssueStateGoal || ev.Assignee != "alpha:librarian" {
		t.Fatalf("assign payload = %+v, %v", ev, ok)
	}
	if msgs[1].Deliverable() || len(msgs[1].To) != 2 || msgs[1].To[1] != "alpha:librarian" {
		t.Fatalf("assign post = %+v", msgs[1])
	}
}

// The registry keeps the wake rules the free-text posts had: a goal row is
// quiet owner-feed input, and a check-in wakes the parent it names.
func TestGoalAndCheckinWakeRules(t *testing.T) {
	s, rec, _ := pinFixture(t)
	for _, tc := range []struct {
		name        string
		event       Event
		to          []string
		wake        bool
		deliverable bool
	}{
		{
			name:  "goal",
			event: GoalEvent{Type: GoalFinished, GoalID: "g1", Title: "Ship the contract", Actor: "alpha:lead", State: GoalStateDone, Summary: "Merged"},
			to:    []string{rec.Name},
		},
		{
			name: "checkin",
			event: CheckinEvent{
				Parent: "ses_parent", WindowStart: 1792227600000,
				Counts:   CheckinCounts{Idle: 1},
				Children: []CheckinChild{{Name: "child", State: CheckinIdle, Age: "12m", Goal: "g1: work", PR: "no open PRs"}},
			},
			to: []string{rec.Name + ":" + RootAgent}, wake: true,
		},
	} {
		in := PostEventInput{RoomID: rec.ID, Event: tc.event, To: tc.to, SessionIDs: map[string]string{tc.to[0]: "ses_parent"}}
		if tc.name == "checkin" {
			in.Producer = ProducerParentCheckin
		}
		msg, err := s.PostEventInput(in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if msg.Wake != tc.wake || WakesRoot(msg, rec.Name) {
			t.Fatalf("%s: wake = %v, want %v (post %+v)", tc.name, msg.Wake, tc.wake, msg)
		}
		if tc.name == "checkin" {
			ev, ok := msg.Checkin()
			if !ok || ev.Parent != "ses_parent" || ev.Counts.Idle != 1 || len(ev.Children) != 1 || ev.Children[0].Name != "child" {
				t.Fatalf("check-in payload = %+v, %v", ev, ok)
			}
			// No invented sender: the check-in comes from the engine, and the
			// producer is data on the post.
			if msg.From != EngineSender || msg.Notice.Producer != ProducerParentCheckin {
				t.Fatalf("check-in sender = %q, producer = %q", msg.From, msg.Notice.Producer)
			}
		}
	}
}
