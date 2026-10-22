package library

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

func fixture(t *testing.T) string {
	t.Helper()
	storetest.Isolate(t)
	dir := t.TempDir()
	for name, body := range map[string]string{"agents/contract.go": "a", "agents/claude/run.go": "c", "docs/agents.md": "d"} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o600)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "c"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// runner runs dyna library against one fixture repository.
func runner(t *testing.T, data, repo string) func(...string) string {
	t.Helper()
	serveLibrary(t, data, authority.Current())
	return func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		c := Command(Options{DataDir: data})
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs(append(args, "--repo", repo))
		if err := c.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
}

// seeded returns a fixture repo whose map has a parent and a verified child.
// Each test below runs at most two commands, so it stays fast on slow CI.
func seeded(t *testing.T) func(...string) string {
	t.Helper()
	data, repo := t.TempDir(), fixture(t)
	r, err := repoid.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := repoid.Git(context.Background(), repo, "rev-parse", "HEAD")
	s := library.Store{DataDir: data, Actor: authority.Current()}
	s.Add(r.ID, library.Entry{ID: "agent-harnesses", Name: "Agent harnesses", Kind: "feature", Summary: "Runs coding agents.", Owns: []string{"agents/*.go"}})
	s.Add(r.ID, library.Entry{ID: "claude-code", Name: "Claude Code", Kind: "adapter", Parent: "agent-harnesses", Summary: "Runs Claude Code.", Owns: []string{"agents/claude/**"}, VerifiedAt: head})
	return runner(t, data, repo)
}

func TestLibraryAdd(t *testing.T) {
	must := runner(t, t.TempDir(), fixture(t))
	must("add", "agent-harnesses", "--name", "Agent harnesses", "--summary", "Runs coding agents behind one contract.", "--owns", "agents/*.go")
	out := must("add", "claude-code", "--name", "Claude Code", "--kind", "adapter", "--parent", "agent-harnesses", "--summary", "Runs Claude Code.", "--owns", "agents/claude/**")
	if !strings.Contains(out, "claude-code · Claude Code (adapter)\nRuns Claude Code.\n") || !strings.Contains(out, "Parent:   agent-harnesses — Runs coding agents behind one contract.") {
		t.Fatalf("added entry:\n%s", out)
	}
}

func TestLibraryLinkAndUpdate(t *testing.T) {
	must := seeded(t)
	must("link", "claude-code", "--docs", "docs/agents.md")
	show := must("update", "claude-code", "--differs", "Uses the Claude CLI.", "--verified", "HEAD")
	for _, want := range []string{"Differs: Uses the Claude CLI.", "Docs:\n  docs/agents.md\n", "Verified at "} {
		if !strings.Contains(show, want) {
			t.Fatalf("entry lacks %q:\n%s", want, show)
		}
	}
}

func TestLibraryListTreeAndStale(t *testing.T) {
	must := seeded(t)
	if tree := must("list", "--tree"); !strings.Contains(tree, "agent-harnesses  Runs") || !strings.Contains(tree, "\n  claude-code  Runs Claude Code.") {
		t.Fatalf("tree:\n%s", tree)
	}
	if stale := must("list", "--stale"); !strings.Contains(stale, "agent-harnesses") || strings.Contains(stale, "claude-code") {
		t.Fatalf("stale:\n%s", stale)
	}
}

func TestLibraryLintAndRetire(t *testing.T) {
	must := seeded(t)
	if lint := must("lint", "--json"); !strings.Contains(lint, `"rule":6,"entry":"agent-harnesses"`) {
		t.Fatalf("lint: %s", lint)
	}
	if out := must("retire", "claude-code"); !strings.Contains(out, "Retired.") {
		t.Fatalf("retire: %s", out)
	}
}

func TestOnlyTheLibrarianSessionWrites(t *testing.T) {
	data, repo := t.TempDir(), fixture(t)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "ses_other")
	var out bytes.Buffer
	serveLibrary(t, data, authority.Current())
	c := Command(Options{DataDir: data})
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs([]string{"add", "x", "--name", "X", "--summary", "X.", "--owns", "agents/**", "--repo", repo})
	if err := c.Execute(); err == nil || !strings.HasPrefix(err.Error(), "library add is ") {
		t.Fatalf("a non-librarian session wrote the map: %v", err)
	}
}

// TestCheckReadmeRejectsALongReadme is the librarian's own gate before it
// opens a docs pull request.
func TestCheckReadmeRejectsALongReadme(t *testing.T) {
	repo := fixture(t)
	limit := 3
	check := func(lines int) error {
		os.WriteFile(filepath.Join(repo, "README.md"), []byte(strings.Repeat("line\n", lines)), 0o600)
		v := settings.Defaults()
		v.LibraryReadmeMaxLines = limit
		c := Command(Options{DataDir: t.TempDir(), Settings: func() (settings.Values, error) { return v, nil }})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs([]string{"check-readme", "--repo", repo})
		return c.Execute()
	}
	if err := check(3); err != nil {
		t.Fatalf("README at the limit: %v", err)
	}
	if err := check(4); err == nil || !strings.Contains(err.Error(), "README.md has 4 lines; library.readmeMaxLines is 3") {
		t.Fatalf("README over the limit: %v", err)
	}
}

// show is what an agent reads after the feature index: summary, how it
// differs, its place in the map, its links, and whether the pin is stale.
func TestLibraryShowReadsAsALookup(t *testing.T) {
	must := seeded(t)
	parent := must("show", "agent-harnesses")
	for _, want := range []string{
		"agent-harnesses · Agent harnesses (feature)\nRuns coding agents.\n",
		"Children: claude-code\n",
		"Files:\n  agents/*.go\n",
		"Not verified against a commit yet.",
	} {
		if !strings.Contains(parent, want) {
			t.Errorf("show agent-harnesses lacks %q:\n%s", want, parent)
		}
	}
	if child := must("show", "claude-code"); !strings.Contains(child, "Parent:   agent-harnesses — Runs coding agents.") || !strings.Contains(child, "Verified at ") || strings.Contains(child, "changed since") {
		t.Fatalf("show claude-code:\n%s", child)
	}
}

// Two clones of one remote share a map. A pin made in one clone can name a
// commit the other lacks; show and the write commands must still print.
func TestShowPrintsWhenThePinIsMissingFromThisClone(t *testing.T) {
	data := t.TempDir()
	clone := func() string {
		dir := fixture(t)
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.com/shared.git").CombinedOutput(); err != nil {
			t.Fatalf("remote add: %v %s", err, out)
		}
		return dir
	}
	a, b := clone(), clone()
	// Only clone a has this commit.
	if out, err := exec.Command("git", "-C", a, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "only in a").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	pinA, _ := repoid.Git(context.Background(), a, "rev-parse", "HEAD")
	r, err := repoid.Resolve(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (library.Store{DataDir: data, Actor: authority.Current()}).Add(r.ID, library.Entry{ID: "agents", Name: "Agents", Kind: "feature", Summary: "Runs agents.", Owns: []string{"agents/*.go"}, VerifiedAt: pinA}); err != nil {
		t.Fatal(err)
	}
	if _, err := repoid.Git(context.Background(), b, "cat-file", "-e", pinA+"^{commit}"); err == nil {
		t.Fatal("clone b has the pinned commit")
	}
	must := runner(t, data, b)
	for _, args := range [][]string{{"show", "agents"}, {"link", "agents", "--docs", "docs/agents.md"}} {
		if out := must(args...); !strings.Contains(out, "agents · Agents (feature)") || !strings.Contains(out, "this checkout cannot check that commit") {
			t.Fatalf("%v:\n%s", args, out)
		}
	}
}
