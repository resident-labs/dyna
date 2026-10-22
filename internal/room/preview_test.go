package room

import (
	"strings"
	"testing"
)

// webp is the smallest file sniffMIME reads as image/webp.
var webp = []byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00 dyna preview")

func TestPostKeepsACardsPreview(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	img, err := s.PutAttachment(rec.ID, "banner.webp", webp)
	if err != nil || img.MIME != "image/webp" {
		t.Fatal(img, err)
	}
	text, _ := s.PutAttachment(rec.ID, "notes.txt", []byte("notes"))
	card, _ := s.PutAttachment(rec.ID, "v.html", []byte("<title>V</title><p>v</p>"))
	good := Attachment{ID: card.ID, Name: "v.html", Render: RenderHTML, Net: true, Banner: img.ID, Thumb: img.ID,
		Shots:  []Shot{{Width: 360, ID: img.ID}, {Width: 768, ID: img.ID}, {Width: 1280, ID: img.ID}},
		Checks: []Check{{Name: "renders", Status: "pass"}, {Name: "widths", Status: "warn", Detail: "Overflow at widths: 360"}}}
	msg, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "t", Attachments: []Attachment{good}})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.Attachments[0]
	if got.Banner != img.ID || got.Thumb != img.ID || len(got.Shots) != 3 || got.Shots[2].Width != 1280 || len(got.Checks) != 2 || got.Checks[1].Detail == "" || !got.Net {
		t.Fatalf("the preview must be kept: %+v", got)
	}
	bad := map[string]func(*Attachment){
		"banner not an image": func(a *Attachment) { a.Banner = text.ID },
		"thumb not saved":     func(a *Attachment) { a.Thumb = strings.Repeat("0", 24) },
		"too many shots":      func(a *Attachment) { a.Shots = append(a.Shots, a.Shots[0]) },
		"unknown status":      func(a *Attachment) { a.Checks = []Check{{Name: "renders", Status: "great"}} },
		"long detail": func(a *Attachment) {
			a.Checks = []Check{{Name: "renders", Status: "fail", Detail: strings.Repeat("x", 300)}}
		},
	}
	for name, change := range bad {
		a := good
		change(&a)
		if _, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "t", Attachments: []Attachment{a}}); err == nil {
			t.Errorf("%s: post must fail", name)
		}
	}
	plain := Attachment{ID: text.ID, Name: "notes.txt", Banner: img.ID}
	if _, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "t", Attachments: []Attachment{plain}}); err == nil {
		t.Error("only a card has a preview")
	}
}

func TestCardCSPBlocksTheNetworkUnlessAllowed(t *testing.T) {
	closed, open := CardCSP(false, "'self'"), CardCSP(true, "'self'")
	if strings.Contains(closed, "https:") || !strings.Contains(closed, "img-src data: blob:") {
		t.Fatalf("a card without --allow-net must not load from the network: %s", closed)
	}
	if !strings.Contains(closed, "script-src 'unsafe-inline';") || !strings.Contains(open, "script-src 'unsafe-inline' https:;") {
		t.Fatalf("scripts load over https only with --allow-net:\n%s\n%s", closed, open)
	}
	if !strings.Contains(open, "connect-src https: data: blob:") {
		t.Fatalf("an allowed card keeps https: %s", open)
	}
	for _, csp := range []string{closed, open} {
		if !strings.HasSuffix(csp, "frame-ancestors 'self'; sandbox allow-scripts") {
			t.Fatalf("csp = %s", csp)
		}
	}
}
