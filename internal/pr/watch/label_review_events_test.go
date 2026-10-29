package watch

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// An approval or a request for changes with an empty body is still a typed
// review result for the author, even when a bot repeats it in the same poll.
func TestEmptyReviewStatesReachAuthorAsTypedEvents(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.addTrustedComment(31, "reviews", "", "maintainer")
	e.f.Comments[len(e.f.Comments)-1].State = "APPROVED"
	e.addTrustedComment(32, "reviews", "", "copilot-pull-request-reviewer[bot]")
	e.f.Comments[len(e.f.Comments)-1].State = "CHANGES_REQUESTED"
	e.addTrustedComment(33, "reviews", "", "copilot-pull-request-reviewer[bot]")
	e.f.Comments[len(e.f.Comments)-1].State = "CHANGES_REQUESTED"
	e.tick()
	r := e.record()
	var got *Notice
	for i, n := range r.Notices {
		if strings.Contains(n.Text, "approved this PR.") {
			got = &r.Notices[i]
		}
	}
	if got == nil {
		t.Fatal("no notice carries the approval")
	}
	if got.Type != room.PRChanges {
		t.Fatalf("review batch type = %q, want %q", got.Type, room.PRChanges)
	}
	if n := strings.Count(got.Text, "requested changes on this PR."); n != 2 {
		t.Fatalf("notice holds %d change requests, want 2: %q", n, got.Text)
	}
}

// A label change is a typed PR update that tells the author what changed.
func TestLabelChangeReachesAuthorAsPRUpdate(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.f.Pull.Labels = []Label{{Name: "needs-tests"}}
	e.tick()
	for _, n := range e.record().Notices {
		if strings.Contains(n.Text, "added needs-tests") {
			if n.Type != room.PRUpdate {
				t.Fatalf("label notice type = %q, want %q", n.Type, room.PRUpdate)
			}
			return
		}
	}
	t.Fatal("no notice reports the added label")
}

// Adding the same label again after a removal is a new change for the author.
func TestRepeatedLabelAddReachesAuthorAgain(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.tick()
	steps := []struct {
		updated string
		labels  []Label
	}{
		{"2026-10-29T14:00:00Z", []Label{{Name: "needs-tests"}}},
		{"2026-10-29T14:01:00Z", nil},
		{"2026-10-29T14:02:00Z", []Label{{Name: "needs-tests"}}},
	}
	for _, s := range steps {
		e.f.Pull.Updated, e.f.Pull.Labels = s.updated, s.labels
		e.tick()
	}
	added := 0
	for _, n := range e.record().Notices {
		if strings.Contains(n.Text, "added needs-tests") {
			added++
		}
	}
	if added != 2 {
		t.Fatalf("label add notices = %d, want 2", added)
	}
}
