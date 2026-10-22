package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintReportsEachRule builds one fixture repo that breaks rules 1 to 7
// once each, and one clean entry that breaks none.
func TestLintReportsEachRule(t *testing.T) {
	dir := gitRepo(t, map[string]string{
		"README.md":          "See [agents](docs/agents.md), [gone](docs/gone.md), [web](https://x.dev), [mail](mailto:a@b.c), [guide](docs/user%20guide.md?x=1#top) and [lost](docs/lost%20guide.md).\nMore.",
		"docs/user guide.md": "Guide.",
		"docs/agents.md":     "Back to [readme](../README.md#top). The contract is in `agents/contract.go`; retries live in `agents/retry.txt`. Upstream keeps `agents/upstream.go`. See `~/.cache/x`, `$HOME/y` and `a/b`.",
		"agents/contract.go": "package agents",
		"agents/claude/a.go": "package claude",
		"agents/codex/b.go":  "package codex",
		"agents/a_test.go":   "package agents",
		"shared/util.go":     "package shared",
		"orphan/lost.go":     "package orphan",
		"store/store.go":     "package store",
		"assets/logo.png":    "png",
		"agents/retry.txt":   "notes",
	})
	pin := run(t, dir, "rev-parse", "HEAD")
	write(t, dir, map[string]string{"store/store.go": "package store // changed"})
	// agents/retry.txt existed and is gone: a stale mention. agents/upstream.go
	// never existed here, like a path in another project's docs: no finding.
	if err := os.Remove(filepath.Join(dir, "agents", "retry.txt")); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	e := func(id, parent, kind string, owns ...string) Entry {
		return Entry{ID: id, Name: id, Kind: kind, Parent: parent, Summary: "Does " + id + ".", Owns: owns, VerifiedAt: pin}
	}
	harness := e("agent-harnesses", "", "feature", "agents/*.go")
	harness.Docs, harness.Tests = []string{"docs/agents.md"}, []string{"agents/*_test.go"}
	claude := e("claude-code", "agent-harnesses", "adapter", "agents/claude/**", "shared/**")
	claude.Differs = "Runs Claude."
	codex := e("codex", "agent-harnesses", "adapter", "agents/codex/**", "shared/**") // rule 1 (shared/util.go) and rule 5 (no differs)
	empty := e("empty", "", "service", "nothing/**")                                  // rule 3
	empty.Docs = []string{"docs/missing.md"}                                          // rule 4 (map side)
	long := e("store", "", "service", "store/**")                                     // rule 6 (store.go changed after pin)
	long.Summary = strings.Repeat("word ", 60) + "."                                  // rule 5 (length)
	lost := e("lost-child", "ghost", "feature", "agents/contract.go")                 // rule 7; also co-owns contract.go with no relation
	lost.Relates = []string{"agent-harnesses"}

	fs, err := Lint(context.Background(), dir, []Entry{harness, claude, codex, empty, long, lost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range fs {
		key := fmt.Sprintf("%d %s %s", f.Rule, f.Entry, f.File)
		for n := 2; got[key]; n++ {
			key = fmt.Sprintf("%d %s %s#%d", f.Rule, f.Entry, f.File, n)
		}
		got[key] = true
	}
	want := []string{
		"1 claude-code shared/util.go",
		"2  orphan/lost.go",
		"3 empty ",
		"4 empty docs/missing.md",
		"4  README.md",
		"4  docs/agents.md",
		"9  README.md",
		"4  README.md#2",
		"5 codex ",
		"5 store ",
		"6 store ",
		"7 lost-child ",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing finding %q", w)
		}
	}
	if len(fs) != len(want) {
		t.Errorf("got %d findings, want %d: %+v", len(fs), len(want), fs)
	}
}
