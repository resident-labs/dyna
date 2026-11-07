package prs

import (
	"strings"
	"testing"
)

// Regressions from the independent review probes for PR #2183.
func TestIndentedHTMLDoesNotSpendExcerptBudget(t *testing.T) {
	text := "<picture>\n    <img src=\"" + strings.Repeat("x", 2100) + "\">\n</picture>\nUseful review"
	got := shorten(text)
	if got != "Useful review" {
		t.Fatalf("HTML still displaces useful text: %d runes; prefix %.50q", len([]rune(got)), got)
	}
}
func TestMarkdownCodeDelimiters(t *testing.T) {
	for _, text := range []string{
		"```html\nconst text = \"```\";\n<div>example</div>\n```",
		"``a ``` ` <b>x</b>``",
		"```html\n<div>unclosed fence example</div>",
		"````html\n```\n<p>longer fence</p>\n````",
		"~~~html\n<p>unclosed tilde fence</p>",
		"```html\n``` not a closing fence\n<p>still code</p>\n```",
		"```html\n    ```\n<p>indented delimiter is not a closing fence</p>\n```",
		"> ```html\n> <p>quoted code</p>\n> ```",
		"- ```html\n  <p>list code</p>\n  ```",
	} {
		if got := shorten(text); got != text {
			t.Errorf("got %q; want %q", got, text)
		}
	}
}
