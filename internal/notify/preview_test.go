package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/visual"
)

func TestPostSavesACardsPreview(t *testing.T) {
	f := newFixture(t)
	webp := func(tag string) []byte { return []byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00 " + tag) }
	preview := visual.Result{Banner: webp("banner"), Thumb: webp("thumb"),
		Widths: []visual.Shot{{Width: 360, Image: webp("360")}, {Width: 1280, Image: webp("1280")}},
		Checks: []visual.Check{{Name: "renders", Status: "pass"}, {Name: "network", Status: "pass"}}}
	res, err := f.svc.Post(f.ctx, PostInput{Sender: f.session(f.decoder), Mentions: []string{"@" + f.a.Name}, Text: "chart",
		Files: []File{{Name: "chart.html", Data: []byte("<title>Chart</title><svg></svg>"), Render: room.RenderHTML, AllowNet: true, Preview: preview}}})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Message.Attachments
	if len(a) != 1 {
		t.Fatalf("preview images are not post attachments: %+v", a)
	}
	c := a[0]
	if !c.Net || c.Banner == "" || c.Thumb == "" || c.Banner == c.Thumb || len(c.Shots) != 2 || c.Shots[1].Width != 1280 || len(c.Checks) != 2 {
		t.Fatalf("card = %+v", c)
	}
	if path, err := f.svc.Rooms.AttachmentPath(f.a.ID, c.Shots[0].ID); err != nil || path == "" {
		t.Fatal(path, err)
	}
	// No browser: the card posts with skipped checks and no images.
	res, err = f.svc.Post(f.ctx, PostInput{Sender: f.session(f.decoder), Mentions: []string{"@" + f.a.Name}, Text: "chart",
		Files: []File{{Name: "chart.html", Data: []byte("<p>x</p>"), Render: room.RenderHTML, Preview: visual.Result{Checks: []visual.Check{{Name: "renders", Status: "skip", Detail: "preview unavailable"}}}}}})
	if err != nil || res.Message.Attachments[0].Banner != "" || res.Message.Attachments[0].Net {
		t.Fatal(res.Message.Attachments, err)
	}

	// A preview image over the file limit is dropped; the card still posts.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := settings.Defaults()
	v.Set(settings.KeyMaxFileBytes, "40")
	v.Set(settings.KeyMaxCardBytes, "1024")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	big := visual.Result{Banner: append(webp("banner"), make([]byte, 100)...), Checks: []visual.Check{{Name: "renders", Status: "pass"}}}
	res, err = f.svc.Post(f.ctx, PostInput{Sender: f.session(f.decoder), Mentions: []string{"@" + f.a.Name}, Text: "chart",
		Files: []File{{Name: "chart.html", Data: []byte("<title>Big</title><p>x</p>"), Render: room.RenderHTML, Preview: big}}})
	if err != nil {
		t.Fatalf("a preview that cannot be saved must not block the post: %v", err)
	}
	if c := res.Message.Attachments[0]; c.Banner != "" || len(c.Checks) != 5 || c.Checks[0].Status != "skip" || !strings.Contains(c.Checks[0].Detail, "preview not saved") {
		t.Fatalf("card = %+v", c)
	}
}
