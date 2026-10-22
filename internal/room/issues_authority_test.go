package room

import (
	"errors"
	"strings"
	"testing"
)

func TestIssueTriageUsesSenderAuthority(t *testing.T) {
	s, rec, _ := pinFixture(t)
	item, err := s.FileIssue(rec.ID, "Triage", "", "", 0, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	reached := errors.New("triage callback reached")
	for _, process := range []string{"", "root", "subagent", "workflow"} {
		t.Run(process, func(t *testing.T) {
			t.Setenv("DYNA_MODE", process)
			t.Setenv("DYNA_SESSION", "ses-test")
			for _, sender := range []string{rec.Name, rec.Name + ":root", rec.Name + ":agent", "other:root", ""} {
				called := false
				_, err := s.ChangeIssue(rec.ID, item.ID, sender, func(i *Issue) error {
					called = true
					return reached
				})
				allowed := sender == rec.Name || sender == rec.Name+":root"
				if called != allowed || errors.Is(err, reached) != allowed {
					t.Fatalf("sender %q: called=%v, err=%v", sender, called, err)
				}
				if !allowed && !strings.HasPrefix(err.Error(), "ChangeIssue is ") {
					t.Errorf("sender %q refusal: %v", sender, err)
				}
			}
		})
	}
}
