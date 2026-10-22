package roomsvc

import (
	"testing"

	"dyna-agent/internal/room"
)

func TestSendToSessionWithAttachment(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	a, err := e.svc.Attach("alpha", "log.txt", []byte("line one"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.SendToSession(e.ctx, lead.ID, "", "", room.Attachment{ID: a.ID, Name: "build log.txt"})
	if err != nil || res.Message.Attachments[0].Name != "build log.txt" || !res.Message.Human {
		t.Fatalf("message = %+v, %v", res.Message, err)
	}
	e.wait(lead.ID, 2)
}

func TestDraftStickyKeepsExplicitRecipients(t *testing.T) {
	members := []string{"viewer", "lead"}
	for draft, want := range map[string]string{
		"@viewer done, thanks":        "@viewer ",
		"Thanks @viewer":              "@viewer ",
		"@viewer @lead both of you":   "@viewer @lead ",
		"no mention goes to the team": "",
		"see `@lead` in the code":     "",
	} {
		if got := ParseDraft(draft, "alpha", members).Sticky(); got != want {
			t.Errorf("Sticky(%q) = %q, want %q", draft, got, want)
		}
	}
}
