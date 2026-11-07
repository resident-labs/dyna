package prs

import (
	"strings"
	"testing"
)

// Regressions from the second independent review of PR #2183.
func TestMultilineHTMLInMarkdownContainers(t *testing.T) {
	input := "> text <a\n> href=\"x\">link</a>"
	if got := shorten(input); strings.Contains(got, "href=") {
		t.Errorf("HTML attr leaked: %q", got)
	}
	input = "> <img\n> src=\"" + strings.Repeat("x", 2100) + "\">\n> Useful review"
	if got := shorten(input); !strings.Contains(got, "Useful review") {
		t.Errorf("HTML consumes budget: %d runes prefix %.40q", len([]rune(got)), got)
	}
	for _, test := range []struct{ input, want string }{
		{"> text <a\n> href=\"x\">link</a>", "> text \n> link"},
		{"> <img\n> src=\"x\">\n> Useful review", "> \n> \n> Useful review"},
		{"> > text <a\n> > href=\"x\">link</a>", "> > text \n> > link"},
		{"- text <a\n  href=\"x\">link</a>", "- text \n  link"},
		{"> <!-- hidden\n> comment -->\n> Useful review", "> \n> \n> Useful review"},
		{"> <picture>\n>     <img\n>       src=\"x\">\n> </picture>\n> Useful review", "> \n>     \n> \n> \n> Useful review"},
	} {
		if got := shorten(test.input); got != test.want {
			t.Errorf("cleanup of %q: got %q, want %q", test.input, got, test.want)
		}
	}
}
