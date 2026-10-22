package room

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/visual"
)

func noPreview(t *testing.T) {
	saved := preview
	preview = func(context.Context, []byte, bool) visual.Result { return visual.Result{} }
	t.Cleanup(func() { preview = saved })
}

func TestPostUpdateAddsAVersion(t *testing.T) {
	noPreview(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	if _, err := store().Create(room.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.html"), filepath.Join(dir, "two.html")
	os.WriteFile(one, []byte("<title>Chart</title><p>1</p>"), 0600)
	os.WriteFile(two, []byte("<title>Chart</title><p>2</p>"), 0600)
	out, err := execCmd(t, PostCmd(), "@work", "--html", one, "first")
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`visual (msg_[0-9a-f]+) v1;`).FindStringSubmatch(out)
	if id == nil {
		t.Fatalf("a new card must print its visual ID and the update command:\n%s", out)
	}
	out, err = execCmd(t, PostCmd(), "@work", "--html", two, "--update", id[1], "second")
	if err != nil || !strings.Contains(out, "visual "+id[1]+" v2;") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if _, err := execCmd(t, PostCmd(), "@work", "--update", id[1], "no file"); err == nil {
		t.Fatal("--update without --html must fail")
	}
}

func TestPostPrintsChecksAndPreviewPaths(t *testing.T) {
	webp := func(tag string) []byte { return []byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00 " + tag) }
	saved := preview
	preview = func(_ context.Context, _ []byte, allowNet bool) visual.Result {
		return visual.Result{Banner: webp("banner"), Thumb: webp("thumb"),
			Widths: []visual.Shot{{Width: 360, Image: webp("360")}, {Width: 1280, Image: webp("1280")}},
			Checks: []visual.Check{{Name: "renders", Status: "pass"}, {Name: "widths", Status: "warn", Detail: "Overflow at widths: 360"}}}
	}
	t.Cleanup(func() { preview = saved })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	if _, err := store().Create(room.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(t.TempDir(), "page.html")
	os.WriteFile(page, []byte(`<title>Wide</title><p>wide</p>`), 0600)
	out, err := execCmd(t, PostCmd(), "@work", "--html", page, "wide page")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"checks: 1 passed, 1 warning\n", "    warn widths: Overflow at widths: 360", "    banner   /", "    360 px   /", "    1280 px  /", ".webp"} {
		if !strings.Contains(out, want) {
			t.Fatalf("post output lacks %q:\n%s", want, out)
		}
	}
	if _, err := execCmd(t, PostCmd(), "@work", "--allow-net", "no file"); err == nil {
		t.Fatal("--allow-net without --html must fail")
	}
}
