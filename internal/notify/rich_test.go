package notify

import (
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// The --html intent rides on File into the record. PutAttachment dedupes on
// bytes, so the same file must keep each post's own choice.
func TestPostFileRenderSurvivesDedupe(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := settings.Defaults()
	v.Set(settings.KeyMaxCardBytes, "1024")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	snd, to := f.session(f.decoder), []string{"@" + f.a.Name}
	doc := []byte(`<title>Chart</title><meta name="dyna-height" content="240"><svg></svg>`)
	post := func(text string, files ...File) (room.Message, error) {
		res, err := f.svc.Post(f.ctx, PostInput{Sender: snd, Mentions: to, Text: text, Files: files})
		return res.Message, err
	}
	for i, render := range []string{"", room.RenderHTML, ""} {
		msg, err := post("the chart", File{Name: "chart.html", Data: doc, Render: render})
		if err != nil {
			t.Fatal(err)
		}
		a := msg.Attachments[0]
		if a.Rich() != (render != "") || (a.Rich() && (a.Title != "Chart" || a.Height != 240)) {
			t.Fatalf("post %d (render %q) stored %+v", i, render, a)
		}
	}

	dir, _ := f.svc.Rooms.FilesDir(f.a.ID)
	before, _ := os.ReadDir(dir)
	refused := map[string]File{
		"no text": {Name: "chart.html", Data: doc, Render: room.RenderHTML},
		"too big": {Name: "big.html", Data: []byte(strings.Repeat("x", 1025)), Render: room.RenderHTML},
		"refresh": {Name: "r.html", Data: []byte(`<meta http-equiv=refresh content=0>`), Render: room.RenderHTML},
		"base":    {Name: "b.html", Data: []byte(`<base href="https://example.com/">`), Render: room.RenderHTML},
		"latin-1": {Name: "l.html", Data: []byte{'<', 'p', '>', 0xe9}, Render: room.RenderHTML},
	}
	for name, file := range refused {
		text := "summary"
		if name == "no text" {
			text = ""
		}
		if _, err := post(text, file); err == nil {
			t.Errorf("%s: an --html post must fail", name)
		}
	}
	if _, err := post("two", File{Name: "a.html", Data: doc, Render: room.RenderHTML}, File{Name: "b.html", Data: []byte("<p>b</p>"), Render: room.RenderHTML}); err == nil {
		t.Error("two cards in one post must fail")
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatalf("refused cards saved files: %d before, %d after", len(before), len(after))
	}
	// As plain files, the same bytes stay sendable for source review.
	for name, file := range refused {
		file.Render = ""
		if _, err := post("source", file); err != nil {
			t.Errorf("%s: a plain --attach must still work: %v", name, err)
		}
	}
}
