package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/visual"
)

func TestVisualCheckReportsProblemsAndWritesImages(t *testing.T) {
	doc := []byte(`<title>Broken chart</title><p>Chart</p><script>console.error("broken")</script>`)
	page := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(page, doc, 0600); err != nil {
		t.Fatal(err)
	}
	saved := preview
	t.Cleanup(func() { preview = saved })
	for _, status := range []string{"pass", "fail", "warn", "skip"} {
		t.Run(status, func(t *testing.T) {
			scratch := t.TempDir()
			t.Setenv("DYNA_SCRATCH", scratch)
			called := false
			preview = func(_ context.Context, got []byte, allowNet bool) visual.Result {
				called = true
				if string(got) != string(doc) || !allowNet {
					t.Fatal("renderer must receive the HTML and --allow-net")
				}
				return visual.Result{Banner: []byte("banner"), Thumb: []byte("thumbnail"),
					Widths: []visual.Shot{{Width: 360, Image: []byte("360")}, {Width: 768, Image: []byte("768")}, {Width: 1280, Image: []byte("1280")}},
					Checks: []visual.Check{{Name: "console", Status: status, Detail: "broken"}}}
			}
			out, err := execCmd(t, VisualCmd(), "check", page, "--allow-net")
			if !called || (err != nil) != (status != "pass") {
				t.Fatalf("called=%v, error=%v; output:\n%s", called, err, out)
			}
			if !strings.Contains(out, status+" console: broken") {
				t.Fatalf("missing check: %s", out)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 1 || !entries[0].IsDir() {
				t.Fatalf("scratch output: %v, %v", entries, err)
			}
			dir := filepath.Join(scratch, entries[0].Name())
			if !strings.Contains(out, dir) {
				t.Fatalf("missing output directory: %s", out)
			}
			for name, want := range map[string]string{"banner": "banner", "thumbnail": "thumbnail", "360": "360", "768": "768", "1280": "1280"} {
				path := filepath.Join(dir, name+".webp")
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want || !strings.Contains(out, path) {
					t.Fatalf("image %s: %q, %v; output: %s", path, got, err, out)
				}
			}
		})
	}
}
