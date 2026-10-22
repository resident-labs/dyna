package watch

import (
	"os"
	"strings"
	"testing"
)

// Real CodeRabbit summary comments from #537 (free-plan summary) and #517
// (review limit). They hold no findings and must wake nobody. A summary that
// names actionable comments or holds a code location still wakes the author.
func TestCodeRabbitSummaryWithoutFindingsIsNoise(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	summary, limit := read("coderabbit-summary-537.md"), read("coderabbit-review-limit-517.md")
	for _, tc := range []struct{ name, body, kind, login, want string }{
		{"free-plan summary", summary, "issue-comments", "coderabbitai[bot]", "drop"},
		{"review limit", limit, "issue-comments", "coderabbitai[bot]", "drop"},
		{"actionable comments", strings.Replace(summary, "### Walkthrough", "**Actionable comments posted: 2**\n\n### Walkthrough", 1), "issue-comments", "coderabbitai[bot]", "keep"},
		{"code location", summary + "\nBug at parser.go:12: ensure bounds.", "issue-comments", "coderabbitai[bot]", "keep"},
		{"code fence", summary + "\n```go\nreturn nil\n```", "issue-comments", "coderabbitai[bot]", "keep"},
		{"nonzero findings", summary + "\nFindings: 2", "issue-comments", "coderabbitai[bot]", "keep"},
		{"prose finding", summary + "\nReal finding: the parser reads past the end.", "issue-comments", "coderabbitai[bot]", "keep"},
		{"bare marker with prose finding", codeRabbitSummaryMarker + "\nReal finding: the parser reads past the end.", "issue-comments", "coderabbitai[bot]", "keep"},
		{"html code", summary + "\n<code>buf[n+1]</code>", "issue-comments", "coderabbitai[bot]", "keep"},
		{"html pre", summary + "\n<pre>return nil</pre>", "issue-comments", "coderabbitai[bot]", "keep"},
		{"finding inside walkthrough", strings.Replace(summary, "### Walkthrough", "### Walkthrough\n\nThis change can cause a nil pointer.", 1), "issue-comments", "coderabbitai[bot]", "keep"},
		{"inline comment", summary, "comments", "coderabbitai[bot]", "keep"},
		{"owner", summary, "issue-comments", "owner", "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := (Rules{}).Triage(PRContext{OwnerLogin: "owner"}, []Event{{Text: tc.body, Kind: tc.kind, Commenter: tc.login, IsBot: tc.login != "owner"}})[0]
			if d.Action != tc.want {
				t.Fatalf("%+v, want %s", d, tc.want)
			}
		})
	}
}

// The watcher drops the summary and still delivers a later finding.
func TestCodeRabbitSummaryFakeGH(t *testing.T) {
	body, err := os.ReadFile("testdata/coderabbit-summary-537.md")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Checks, e.f.Runs = nil, nil
	e.track()
	e.addComment(201, "issue-comments", string(body), "coderabbitai[bot]")
	e.tick()
	if len(e.sent) != 0 || e.record().PendingDrops["known_bot_summary"] != 1 {
		t.Fatalf("summary woke the author: %+v %+v", e.sent, e.record().PendingDrops)
	}
	e.addComment(202, "comments", "Bug at parser.go:12: ensure bounds.", "coderabbitai[bot]")
	e.tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "parser.go:12") {
		t.Fatalf("finding lost: %+v", e.sent)
	}
}
