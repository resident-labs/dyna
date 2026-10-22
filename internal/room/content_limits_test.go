package room

import (
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/settings"
)

func setContentLimits(t *testing.T, card, file int) {
	t.Helper()
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for key, n := range map[string]int{settings.KeyMaxCardBytes: card, settings.KeyMaxFileBytes: file} {
		if err := v.Set(key, strconv.Itoa(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentContentLimitsAndPostTotal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, rec := attachRoom(t, "small")
	setContentLimits(t, 30, 4)
	card := []byte("<p>card</p>")
	if _, err := s.PutAttachment(rec.ID, "file", card); err == nil || !strings.Contains(err.Error(), "maximum 4 bytes") {
		t.Fatalf("file limit: %v", err)
	}
	a, err := s.PutRenderedAttachment(rec.ID, "c.html", card, RenderHTML)
	if err != nil {
		t.Fatal(err)
	}
	a.Render = RenderHTML
	atts := []Attachment{a}
	for i := 0; i < 7; i++ {
		f, err := s.PutAttachment(rec.ID, "f", []byte{byte(i), 1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		atts = append(atts, f)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "small:lead", To: []string{"small"}, Text: "card and files", Attachments: atts}); err != nil {
		t.Fatal(err)
	}
	setContentLimits(t, 9, 30)
	if _, err := ParseRich("c.html", card); err == nil || !strings.Contains(err.Error(), "maximum 9 bytes") {
		t.Fatalf("card limit: %v", err)
	}
	if _, err := s.PutAttachment(rec.ID, "file", card); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "small:lead", To: []string{"small"}, Text: "restaged", Attachments: atts}); err == nil {
		t.Fatal("posted card above new limit")
	}
	setContentLimits(t, 30, 30)
	if _, err := s.Post(rec.ID, PostInput{From: "small:lead", To: []string{"small"}, Text: "larger again", Attachments: atts}); err != nil {
		t.Fatal(err)
	}
}
