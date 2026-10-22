package room

import (
	"os"
	"strings"
	"testing"
)

const cardHTML = `<!doctype html><html><head><title>  Build
 status &amp; tests </title><meta name="dyna-height" content="480px"></head><body>ok</body></html>`

func TestParseRichReadsTitleAndHeight(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setContentLimits(t, 1024, 1024)
	m, err := ParseRich("card.html", []byte(cardHTML))
	if err != nil || m.Title != "Build status & tests" || m.Height != 480 {
		t.Fatalf("meta = %+v, %v", m, err)
	}
	m, err = ParseRich("x.HTM", []byte(`<p>no title</p><meta name=dyna-height content=9999><meta name=dyna-height content=200>`))
	if err != nil || m.Title != "" || m.Height != RichHeightMax {
		t.Fatalf("clamped meta = %+v, %v", m, err)
	}
	if m, _ = ParseRich("x.html", []byte(`<meta name=dyna-height content=tall>`)); m.Height != 0 {
		t.Fatalf("a bad height must leave 0, got %d", m.Height)
	}
	if h := (Attachment{Render: RenderHTML}).CardHeight(); h != RichHeightDefault {
		t.Fatalf("an unset height must draw at the default, got %d", h)
	}
	if h := (Attachment{Render: RenderHTML, Height: 5000}).CardHeight(); h != RichHeightMax {
		t.Fatalf("a stored height must clamp, got %d", h)
	}
	m, _ = ParseRich("x.html", []byte(`<title>`+strings.Repeat("é", 100)+`</title>`))
	if len(m.Title) > maxRichTitleBytes || !strings.HasSuffix(m.Title, "…") {
		t.Fatalf("long title = %q", m.Title)
	}
	for name, doc := range map[string]string{
		"page.txt":     "<p>hi</p>",
		"refresh.html": `<META HTTP-EQUIV="Refresh" content="0;url=https://example.com">`,
		"entity.html":  `<meta http-equiv="&#114;efresh" content="0;url=https://example.com">`,
		"slash.html":   `<meta/http-equiv=refresh content=0>`,
		"base.html":    `<base href="https://example.com/">`,
		"binary.html":  "<p>\x00</p>",
		"big.html":     strings.Repeat("a", 1025),
	} {
		if _, err := ParseRich(name, []byte(doc)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestPostStampsRichFromSavedBytes(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	a, err := s.PutAttachment(rec.ID, "card.html", []byte(cardHTML))
	if err != nil {
		t.Fatal(err)
	}
	if a.MIME != "text/plain" || a.Rich() {
		t.Fatalf("a saved .html file is plain text until a post marks it: %+v", a)
	}
	// The caller may only ask for Render; Title and Height come from the bytes.
	ask := Attachment{ID: a.ID, Name: "card.html", Render: RenderHTML, Title: "<b>lie</b>", Height: 5000}
	msg, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "Build is green.", Attachments: []Attachment{ask}})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.Attachments[0]
	if !got.Rich() || got.MIME != "text/html" || got.Title != "Build status & tests" || got.Height != 480 {
		t.Fatalf("stamped = %+v", got)
	}
	plain, _ := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "source", Attachments: []Attachment{{ID: a.ID, Name: "card.html"}}})
	if p := plain.Attachments[0]; p.Rich() || p.MIME != "text/plain" || p.Title != "" {
		t.Fatalf("an unmarked .html attachment must stay plain: %+v", p)
	}

	b, _ := s.PutAttachment(rec.ID, "two.html", []byte("<p>two</p>"))
	note, _ := s.PutAttachment(rec.ID, "note.txt", []byte("<p>note</p>"))
	for name, in := range map[string]PostInput{
		"no text":      {Attachments: []Attachment{ask}},
		"two cards":    {Text: "t", Attachments: []Attachment{ask, {ID: b.ID, Name: "two.html", Render: RenderHTML}}},
		"not .html":    {Text: "t", Attachments: []Attachment{{ID: note.ID, Name: "note.txt", Render: RenderHTML}}},
		"unknown mark": {Text: "t", Attachments: []Attachment{{ID: b.ID, Name: "two.html", Render: "svg"}}},
	} {
		in.From, in.To = "alpha:lead", []string{"alpha"}
		if _, err := s.Post(rec.ID, in); err == nil {
			t.Errorf("%s: post must fail", name)
		}
	}
	if msgs, _ := s.Messages(rec.ID); len(msgs) != 2 {
		t.Fatalf("refused posts stored messages: %d", len(msgs))
	}
}

func TestFramesNeverCarryRichBytes(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	doc := strings.Replace(cardHTML, "ok", "SECRET-BODY-MARKER"+strings.Repeat(" x", 20000), 1)
	a, _ := s.PutAttachment(rec.ID, "card.html", []byte(doc))
	a.Render = RenderHTML
	msg, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha:qa"}, Text: "Build is green; the card has the per-package table.", Attachments: []Attachment{a}})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.FilesDir(rec.ID)
	o := FrameOptions{Room: "alpha", Reader: "alpha:qa", FilesDir: dir}
	frame := msg.Frame(o)
	for _, want := range []string{"Build is green", `card.html · HTML card "Build status & tests"`, dir + "/" + a.ID + ".txt", "renders only in the Dyna desktop app"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	steer, cut := msg.SteeringFrame(o, 2000)
	if cut || len(steer) > 2000 || strings.Contains(frame, "SECRET-BODY-MARKER") {
		t.Fatalf("frames must stay small and hold no HTML (%d bytes, cut=%v):\n%s", len(steer), cut, steer)
	}
	if b, _ := os.ReadFile(dir + "/" + a.ID + ".txt"); !strings.Contains(string(b), "SECRET-BODY-MARKER") {
		t.Fatal("the source must stay readable from its saved path")
	}
}

func TestCardTitleFallsBackToHeadingThenFileName(t *testing.T) {
	m, _ := ParseRich("x.html", []byte(`<body><h1> Merge <em>queue</em> </h1><h1>Other</h1></body>`))
	if m.Title != "Merge queue" {
		t.Fatalf("an untitled card takes its first h1: %q", m.Title)
	}
	m, _ = ParseRich("x.html", []byte(`<title>Real</title><h1>Heading</h1>`))
	if m.Title != "Real" {
		t.Fatalf("<title> wins: %q", m.Title)
	}
	s, rec := attachRoom(t, "alpha")
	a, _ := s.PutAttachment(rec.ID, "tonight-ui.html", []byte("<p>no title</p>"))
	msg, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "t", Attachments: []Attachment{{ID: a.ID, Name: "tonight-ui.html", Render: RenderHTML}}})
	if err != nil || msg.Attachments[0].Title != "tonight-ui" {
		t.Fatalf("with no title or heading the card is named for its file: %+v %v", msg.Attachments, err)
	}
}
