package room

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func forwardFixture(t *testing.T) (*Store, Record, Message) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, rec := attachRoom(t, "alpha")
	a, err := s.PutAttachment(rec.ID, "shot.png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := s.Post(rec.ID, PostInput{From: "alpha:worker", Kind: "completed", To: []string{"alpha"}, Text: "Tests pass.\nTwo files changed.", Attachments: []Attachment{a}})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec, orig
}

func TestForwardStoresReferenceToTheOriginal(t *testing.T) {
	s, rec, orig := forwardFixture(t)
	fwd, err := s.Post(rec.ID, PostInput{From: "alpha", Human: true, To: []string{"alpha:lead"}, Forward: NewForward(orig), Text: "please review"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Message(rec.ID, fwd.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := got.Forward
	if f == nil || f.ID != orig.ID || f.RoomID != rec.ID || f.From != "alpha:worker" || f.TS != orig.TS || f.Kind != "completed" || f.Text != orig.Text {
		t.Fatalf("forward = %#v", f)
	}
	if len(got.Attachments) != 0 || !reflect.DeepEqual(f.Attachments, orig.Attachments) {
		t.Fatalf("files must be referenced, not copied: own %#v, forwarded %#v", got.Attachments, f.Attachments)
	}
	if orig.Forward != nil {
		t.Fatal("the original must not change")
	}
}

func TestForwardFrameQuotesTheOriginalWithAuthorAndTime(t *testing.T) {
	s, rec, orig := forwardFixture(t)
	fwd, _ := s.Post(rec.ID, PostInput{From: "alpha", Human: true, To: []string{"alpha:lead"}, Forward: NewForward(orig), Text: "please review"})
	dir, _ := s.FilesDir(rec.ID)
	frame := fwd.Frame(FrameOptions{Room: "alpha", Reader: "alpha:lead", FilesDir: dir})
	at := time.UnixMilli(orig.TS).Local().Format("2006-01-02 15:04 MST")
	for _, want := range []string{
		"[dyna message " + fwd.ID + " from alpha (human)]",
		"please review",
		"[Forwarded post " + orig.ID + ", originally written by alpha:worker at " + at + "]",
		"> Tests pass.\n> Two files changed.",
		"[End of forwarded post]",
		"Attachments (1)", "shot.png · image/png", dir + "/",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.Index(frame, "please review") > strings.Index(frame, "[Forwarded post") {
		t.Fatalf("the note must come before the quote:\n%s", frame)
	}
}

func TestForwardWithoutNoteAndSteeringKeepsTheQuote(t *testing.T) {
	s, rec, orig := forwardFixture(t)
	fwd, _ := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Forward: NewForward(orig)})
	frame := fwd.Frame(FrameOptions{Room: "alpha"})
	if !strings.Contains(frame, "\n\n[Forwarded post "+orig.ID) || !strings.Contains(frame, "> Tests pass.") {
		t.Fatalf("frame = %s", frame)
	}
	steer, truncated := fwd.SteeringFrame(FrameOptions{Room: "alpha"}, 4096)
	if truncated || steer != frame {
		t.Fatalf("a short forward must steer whole:\n%s", steer)
	}
	small, truncated := fwd.SteeringFrame(FrameOptions{Room: "alpha"}, 400)
	if !truncated || len(small) > 400 || !strings.Contains(small, "[Forwarded post") {
		t.Fatalf("truncated=%v len=%d:\n%s", truncated, len(small), small)
	}
}

func TestPostIDRetryComparesTheForward(t *testing.T) {
	s, rec, orig := forwardFixture(t)
	in := PostInput{ID: "msg_0123456789abcdef", From: "alpha", To: []string{"alpha:lead"}, Forward: NewForward(orig), Text: "n"}
	if _, err := s.Post(rec.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, in); err != nil {
		t.Fatalf("same retry: %v", err)
	}
	in.Forward = nil
	if _, err := s.Post(rec.ID, in); err == nil {
		t.Fatal("a retry with another forward must be refused")
	}
}
