package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

func TestPostReadsFilesWithEffectiveLimits(t *testing.T) {
	noPreview(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	v := settings.Defaults()
	v.Set(settings.KeyMaxCardBytes, "8")
	v.Set(settings.KeyMaxFileBytes, "4")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.html")
	os.WriteFile(path, []byte("<p>hi</p>"), 0600)
	for flag, limit := range map[string]string{"--attach": "4", "--html": "8"} {
		if _, err := execCmd(t, PostCmd(), "@root", flag, path, "summary"); err == nil || !strings.Contains(err.Error(), "maximum "+limit+" bytes") {
			t.Fatalf("%s: %v", flag, err)
		}
	}
	if _, err := store().Create(room.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	v.Set(settings.KeyMaxCardBytes, "9")
	v.Set(settings.KeyMaxFileBytes, "9")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--attach", "--html"} {
		if _, err := execCmd(t, PostCmd(), "@work", flag, path, "summary"); err != nil {
			t.Fatalf("larger %s: %v", flag, err)
		}
	}
}
