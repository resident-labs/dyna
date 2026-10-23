package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 5, ""},
		{"fits", "hello", 5, "hello"},
		{"cuts", "hello world", 5, "hello…"},
		{"multibyte cut on a rune boundary", "héllo wörld", 5, "héllo…"},
		{"wide runes", "日本語のテキスト", 3, "日本語…"},
		{"newline is kept", "a\nb\nc", 3, "a\nb…"},
	}
	for _, c := range cases {
		if got := Clip(c.in, c.n); got != c.want {
			t.Errorf("%s: Clip(%q, %d) = %q, want %q", c.name, c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(Clip(c.in, c.n)) {
			t.Errorf("%s: Clip(%q, %d) is not valid UTF-8", c.name, c.in, c.n)
		}
	}
}

func TestClipOneLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  a \n\t b  ", "a b"},
		{"one\ntwo", "one t…"},
		{strings.Repeat("x", 10), "xxxxx…"},
	}
	for _, c := range cases {
		if got := ClipOneLine(c.in, 5); got != c.want {
			t.Errorf("ClipOneLine(%q, 5) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  \n\n", ""},
		{"  first  \nsecond", "first"},
		{"\n\n  first\nsecond", "first"},
	}
	for _, c := range cases {
		if got := FirstLine(c.in); got != c.want {
			t.Errorf("FirstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"fits", "hello", 8, "hello"},
		{"cuts ascii", "hello", 3, "hel"},
		{"stops before a split rune", "héllo", 2, "h"},
		{"keeps a whole rune", "héllo", 3, "hé"},
		{"zero", "hello", 0, ""},
	}
	for _, c := range cases {
		if got := Prefix(c.in, c.n); got != c.want {
			t.Errorf("%s: Prefix(%q, %d) = %q, want %q", c.name, c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(Prefix(c.in, c.n)) {
			t.Errorf("%s: Prefix(%q, %d) is not valid UTF-8", c.name, c.in, c.n)
		}
	}
}
