package visual

import "testing"

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
