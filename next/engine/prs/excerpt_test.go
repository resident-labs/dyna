package prs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCodeRabbitExcerpts(t *testing.T) {
	for _, name := range []string{"comment", "review"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile("testdata/coderabbit-" + name + ".md")
			if err != nil {
				t.Fatal(err)
			}
			body := string(source)
			got := shorten(body)
			// The real comment's risk assessment starts beyond the original budget.
			if name == "comment" && !strings.Contains(got, "This refactor moves attachment storage") {
				t.Fatalf("boilerplate displaced useful markdown beyond the %d-character budget: %.100s", maxCommentText, got)
			}
			if strings.Contains(got, "<!--") || strings.Contains(got, "<picture>") || strings.Contains(got, "<summary>") {
				t.Fatalf("HTML boilerplate spent the excerpt budget: %.100s", got)
			}
			if len([]rune(got)) != maxCommentText+1 || !strings.HasSuffix(got, "…") {
				t.Fatalf("excerpt length = %d", len([]rune(got)))
			}
			if name == "comment" {
				if !strings.HasPrefix(got, "### Walkthrough\n\nThe pull request adds horizon-mod-runtime") {
					t.Fatalf("missing useful markdown: %.100s", got)
				}
			} else if !strings.Contains(got, "**Actionable comments posted: 3**") || !strings.Contains(got, "Prompt to fix all issues") {
				t.Fatalf("review markdown lost: %s", got)
			}

			// The issue-comment and inline-review-comment feeds supply github.comment and tracked PR changes.
			for _, action := range []contract.GitHubAction{contract.GitHubCommented, contract.GitHubReviewCommented} {
				data, err := json.Marshal([]map[string]any{{"id": 1, "body": body, "html_url": "https://github.com/arismoko/horizon/pull/1624#comment", "issue_url": "https://api.github.com/repos/arismoko/horizon/issues/1624", "user": map[string]string{"login": "coderabbitai[bot]"}}})
				if err != nil {
					t.Fatal(err)
				}
				entries, err := parseComments(action)(data, "arismoko/horizon", "viewer")
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].item.Text != got {
					t.Fatalf("feed excerpt differs: %+v", entries)
				}
			}
			// Reviews queried for github.pr.changed must apply the same cleanup.
			seen := observed(func(o *Observed) { o.Reviews = []Review{{ID: 1, Author: "coderabbitai[bot]", Body: body}} })
			update, _ := ComparePR(tracked(nil), nil, nil, seen, "viewer", time.Now())
			if update.Change == nil || len(update.Change.Comments) != 1 || update.Change.Comments[0].Body != got {
				t.Fatalf("review excerpt differs: %+v", update.Change)
			}
			if seen.Reviews[0].Body != string(source) {
				t.Fatal("source body changed")
			}
		})
	}
}

func TestShortenPreservesMarkdown(t *testing.T) {
	for _, text := range []string{
		"### Heading\n\n**bold** and [link](https://example.com/a?q=1&b=2)",
		"Use `a < b && c > d`, `Vec<T>`, and ``<img src=\"x\">``.",
		"```html\n<!-- example -->\n<img src=\"example\">\n```",
		"~~~html\n<p>example</p>\n~~~",
		"    <p>indented code</p>\n\t<!-- also code -->",
		"Compare a < b and c > d; <https://example.com> and <dev@example.com>.",
		"  indented text\nnext line\n\n- list\n  continuation",
	} {
		if got := shorten(text); got != text {
			t.Errorf("markdown changed:\ngot %q\nwant %q", got, text)
		}
	}
	if got := shorten("\n<!-- hidden\ncomment -->\n\n<a href=\"x>y\"><picture><img src='x'></picture></a>\n\n### Heading\n \t\n\n\nText\n\n"); got != "### Heading\n\nText" {
		t.Fatalf("cleanup = %q", got)
	}
	if got := shorten(strings.Repeat("<!-- padding -->\n\n", 200) + "Useful"); got != "Useful" {
		t.Fatalf("cleanup must precede shortening: %q", got)
	}
	if got := shorten(strings.Repeat("界", maxCommentText+1)); got != strings.Repeat("界", maxCommentText)+"…" {
		t.Fatal("rune limit changed")
	}
}
