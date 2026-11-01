package visual

import (
	"strings"
	"testing"
)

func TestTheme(t *testing.T) {
	for _, mode := range []string{"light", "dark"} {
		got := string(Theme([]byte("<head></head>"), mode))
		if !strings.Contains(got, "html{color-scheme:"+mode+";background:transparent}") {
			t.Fatalf("%s: %s", mode, got)
		}
	}
	declared := []byte(`<meta name="color-scheme" content="light">`)
	if string(Theme(declared, "dark")) != string(declared) {
		t.Fatal("changed the card's declared scheme")
	}
	if strings.Contains(string(Theme([]byte("<p>x"), "dark}</style>")), "<style>") {
		t.Fatal("accepted an invalid theme")
	}
}

func TestColorScheme(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"head", "<head><title>x</title></head>", `<head><meta name="color-scheme" content="light dark"><title>x</title></head>`},
		{"declared", `<head><meta name="color-scheme" content="light"></head>`, `<head><meta name="color-scheme" content="light"></head>`},
		{"no head", "<!doctype html><p>x</p>", `<!doctype html><meta name="color-scheme" content="light dark"><p>x</p>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(ColorScheme([]byte(tc.in))); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
