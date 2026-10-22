package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/worktrees"
)

func TestAddWorktreeRetainsCleanCommittedChanges(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	base := worktreeTestGit(t, repo, "rev-parse", "HEAD")

	wt, gotBase, cleanup, err := worktrees.Create(worktreeTestContext(t), repo, settings.WorktreesRetentionChanged)
	if err != nil {
		t.Fatal(err)
	}
	if gotBase != base {
		t.Fatalf("worktrees.Create() base = %q, want %q", gotBase, base)
	}
	t.Cleanup(func() {
		removeWorktreeTestTree(t, repo, wt)
		os.RemoveAll(wt)
	})

	if err := os.WriteFile(filepath.Join(wt, "committed.txt"), []byte("committed change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, wt, "add", "committed.txt")
	worktreeTestGit(t, wt, "commit", "-m", "commit isolated work")
	if status := worktreeTestGit(t, wt, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree status = %q, want clean", status)
	}
	if head := worktreeTestGit(t, wt, "rev-parse", "HEAD"); head == base {
		t.Fatal("committed worktree HEAD did not advance from its base")
	}

	if retained := cleanup(); !retained {
		t.Fatal("cleanup removed a clean worktree containing a new commit")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("retained worktree is unavailable: %v", err)
	}
}

func TestAgentIsolationRequiresWorktreeObject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "mock-worktree", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "string form",
			script:  `return await agent("task", {isolation: "worktree"});`,
			wantErr: `agent isolation must be an object like { type: "worktree", retention?: "changed" | "all" | "none" }; the string form is not supported`,
		},
		{
			name:    "unknown type",
			script:  `return await agent("task", {isolation: {type: "container"}});`,
			wantErr: `unknown isolation type "container"`,
		},
		{
			name:    "invalid retention",
			script:  `return await agent("task", {isolation: {type: "worktree", retention: "sometimes"}});`,
			wantErr: `agent isolation.retention must be one of changed, all, none`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Execute(worktreeTestContext(t), Options{ScriptSrc: tc.script, Store: store, WorkDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Execute() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestExecuteJournalsRetainedWorktreeGitChanges(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-worktree-git-journal")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	repo := initWorktreeTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "deleted.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "modified.txt"), []byte("old one\nold two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, repo, "add", "deleted.txt", "modified.txt")
	worktreeTestGit(t, repo, "commit", "-m", "add worktree fixtures")
	base := worktreeTestGit(t, repo, "rev-parse", "HEAD")

	const script = `return await agent("change isolated files", {profile: "worktree-writer", label: "writer", isolation: {type: "worktree"}});`
	run, err := runstore.Create("engine worktree git journal test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "worktree-writer", Harness: profile.HarnessCustom, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
		Command: []string{"/bin/sh", "-c", `
printf 'untracked\n' > created.txt
rm deleted.txt
printf 'new one\nnew two\nnew three\n' > modified.txt
printf 'first\n' > committed-one.txt
git add committed-one.txt
git commit -m 'first isolated commit' >/dev/null
printf 'second\n' > committed-two.txt
git add committed-two.txt
git commit -m 'second isolated commit' >/dev/null
printf done
`},
	}}}

	if _, err := Execute(worktreeTestContext(t), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: repo}); err != nil {
		t.Fatal(err)
	}
	rootEntries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootEntries) != 1 || rootEntries[0].Worktree != "retained" || rootEntries[0].Dir == "" {
		t.Fatalf("root journal entries = %#v, want one retained worktree", rootEntries)
	}
	retainedDir := rootEntries[0].Dir
	t.Cleanup(func() {
		removeWorktreeTestTree(t, repo, retainedDir)
		os.RemoveAll(retainedDir)
	})
	records, err := worktrees.ForRun(run.Meta.ID)
	if err != nil || len(records) != 1 {
		t.Fatalf("registered worktrees = %#v, %v", records, err)
	}
	if rec := records[0]; rec.Path != retainedDir || rec.RunID != run.Meta.ID || rec.AgentID != 1 || rec.Label != "writer" || rec.Profile != "worktree-writer" || rec.Repo != repo || rec.Base != base || rec.Head == "" || rec.RetainedAt.IsZero() {
		t.Fatalf("registered worktree = %#v", rec)
	}

	journalPath := filepath.Join(run.Dir, "agents", "1", "journal.jsonl")
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("agent journal was not written at %s: %v", journalPath, err)
	}
	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var commits, diffs []string
	completeIndex := -1
	for i, entry := range agentEntries {
		switch entry.Kind {
		case "git-commit":
			if entry.Source != "system" {
				t.Fatalf("commit entry source = %q, want system", entry.Source)
			}
			commits = append(commits, entry.Message)
		case "git-diff":
			if entry.Source != "system" {
				t.Fatalf("diff entry source = %q, want system", entry.Source)
			}
			diffs = append(diffs, entry.Message)
		case "complete":
			completeIndex = i
		}
	}
	wantCommits := strings.Split(worktreeTestGit(t, retainedDir, "log", "--reverse", "--format=%h %s", base+"..HEAD"), "\n")
	if !reflect.DeepEqual(commits, wantCommits) {
		t.Fatalf("git-commit messages = %#v, want %#v", commits, wantCommits)
	}
	wantDiffs := map[string]bool{
		"created committed-one.txt +1/-0": true,
		"created committed-two.txt +1/-0": true,
		"created created.txt":             true,
		"deleted deleted.txt":             true,
		"modified modified.txt +3/-2":     true,
	}
	if len(diffs) != len(wantDiffs) {
		t.Fatalf("git-diff messages = %#v, want %#v", diffs, wantDiffs)
	}
	for _, message := range diffs {
		if !wantDiffs[message] {
			t.Errorf("unexpected git-diff message %q; want %#v", message, wantDiffs)
		}
	}
	if completeIndex < 0 {
		t.Fatalf("agent journal has no complete entry: %#v", agentEntries)
	}
	for i, entry := range agentEntries {
		if (entry.Kind == "git-commit" || entry.Kind == "git-diff") && i > completeIndex {
			t.Fatalf("git entry at index %d follows complete entry at index %d: %#v", i, completeIndex, agentEntries)
		}
	}
}

func TestPerCallRetentionAllOverridesChangedAndExposesTrustedGlobal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-worktree-all")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	repo := initWorktreeTestRepo(t)
	const script = `
await agent("inspect only", {profile: "mock-worktree", label: "implement:one", isolation: {type: "worktree", retention: "all"}});
const first = worktrees().find(w => w.label === "implement:one");
first.path = "tampered";
return {tampered: first.path, trusted: worktrees().find(w => w.label === "implement:one")};
`
	run, err := runstore.Create("retain all", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "mock-worktree", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}
	result, err := Execute(worktreeTestContext(t), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: repo, WorktreeRetention: settings.WorktreesRetentionChanged})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tampered string `json:"tampered"`
		Trusted  struct {
			ID         int    `json:"id"`
			Label      string `json:"label"`
			Profile    string `json:"profile"`
			Path       string `json:"path"`
			Repo       string `json:"repo"`
			Base       string `json:"base"`
			Head       string `json:"head"`
			RetainedAt string `json:"retainedAt"`
		} `json:"trusted"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result = %q: %v", result, err)
	}
	records, err := worktrees.ForRun(run.Meta.ID)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, %v", records, err)
	}
	rec := records[0]
	t.Cleanup(func() { removeWorktreeTestTree(t, repo, rec.Path) })
	if got.Tampered != "tampered" || got.Trusted.Path != rec.Path || got.Trusted.ID != rec.AgentID || got.Trusted.Label != rec.Label || got.Trusted.Profile != rec.Profile || got.Trusted.Repo != repo || got.Trusted.Base != rec.Base || got.Trusted.Head != rec.Head || got.Trusted.RetainedAt == "" {
		t.Fatalf("worktrees() = %#v; record = %#v", got, rec)
	}
	if rec.Status != "" || rec.Base != rec.Head {
		t.Fatalf("unchanged retention-all fingerprint = %#v", rec)
	}
	entries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil || len(entries) != 1 || entries[0].Worktree != "retained" {
		t.Fatalf("journal = %#v, %v", entries, err)
	}
}

// Retention none cannot override the safety rule that protects dirty trees.
func TestPerCallRetentionNoneRetainsDirtyWorktreeAndJournalsChanges(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-worktree-none")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	repo := initWorktreeTestRepo(t)
	const script = `return await agent("change then discard", {profile: "writer", label: "writer", isolation: {type: "worktree", retention: "none"}});`
	run, err := runstore.Create("retain none", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "writer", Harness: profile.HarnessCustom, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
		Command: []string{"/bin/sh", "-c", `
set -eu
printf 'discarded\n' > discarded.txt
printf '%s\n' '{"ts":1,"kind":"verification","message":"Wrote discarded.txt.","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
printf done
`},
	}}}
	if _, err := Execute(worktreeTestContext(t), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: repo, WorktreeRetention: settings.WorktreesRetentionChanged}); err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil || len(entries) != 1 || entries[0].Worktree != "retained" || entries[0].Dir == "" {
		t.Fatalf("root journal = %#v, %v", entries, err)
	}
	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundDiff := false
	for _, entry := range agentEntries {
		if entry.Kind == "git-diff" && strings.Contains(entry.Message, "discarded.txt") {
			foundDiff = true
		}
	}
	if !foundDiff {
		t.Fatalf("discarded diff was not journaled: %#v", agentEntries)
	}
	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundLog := false
	for _, event := range events {
		if event.T == "agent_nudge" {
			t.Fatalf("worktree fixture requested an unrelated journal reminder: %+v", event)
		}
		if event.T == "log" && event.Provenance == runstore.LogProvenanceSystem && strings.Contains(event.Msg, "worktrees.retention=none") {
			foundLog = true
		}
	}
	if !foundLog {
		t.Fatalf("retention none system log missing: %#v", events)
	}
	records, err := worktrees.ForRun(run.Meta.ID)
	if err != nil || len(records) != 1 || records[0].Path != entries[0].Dir {
		t.Fatalf("retention none did not register the retained tree: %#v, %v", records, err)
	}
	contents, err := os.ReadFile(filepath.Join(records[0].Path, "discarded.txt"))
	if err != nil || string(contents) != "discarded\n" {
		t.Fatalf("retention none did not preserve dirty contents: %q, %v", contents, err)
	}
}

func TestResolveWorktreeRetentionFromSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRetention, settings.WorktreesRetentionAll); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if got, warning, err := resolveWorktreeRetention(""); err != nil || warning != "" || got != settings.WorktreesRetentionAll {
		t.Fatalf("resolveWorktreeRetention() = %q, %q, %v", got, warning, err)
	}
	if _, _, err := resolveWorktreeRetention("sometimes"); err == nil {
		t.Fatal("invalid option retention accepted")
	}
}

func TestExecuteFallsBackWhenSettingsCannotLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{not-json`},
		{name: "newer version", raw: `{"version":999}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("DYNA_RUN_ID", "wf_settings-fallback")
			settingsPath := settings.DefaultPath()
			if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settingsPath, []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}
			run, err := runstore.Create("settings fallback", `return "ok";`, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { run.Finish("ok", "", nil) })
			if _, err := Execute(worktreeTestContext(t), Options{ScriptSrc: `return "ok";`, Store: &profile.Store{}, Run: run}); err != nil {
				t.Fatalf("Execute() rejected unreadable settings: %v", err)
			}
			events, err := runstore.ReadEvents(run.Meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.T == "log" && event.Provenance == runstore.LogProvenanceSystem && strings.Contains(event.Msg, "could not load worktree retention setting") && strings.Contains(event.Msg, `using "changed"`) {
					found = true
				}
			}
			if !found {
				t.Fatalf("settings fallback warning missing: %#v", events)
			}
		})
	}
}

func TestWorktreeGitJournalEntriesCapEachCategory(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	wt, base, _, err := worktrees.Create(worktreeTestContext(t), repo, settings.WorktreesRetentionChanged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		removeWorktreeTestTree(t, repo, wt)
		os.RemoveAll(wt)
	})

	// Import the same real commits in one Git process. Starting a separate
	// add and commit for each entry makes this fixture exceed one second.
	const ref = "refs/heads/journal-cap"
	var history strings.Builder
	for i := 0; i < worktreeJournalEntryLimit+2; i++ {
		message := fmt.Sprintf("isolated commit %02d", i)
		body := fmt.Sprintf("line %d\n", i)
		fmt.Fprintf(&history, "commit %s\ncommitter Dyna Test <dyna-test@example.com> %d +0000\ndata %d\n%s\n", ref, 1700000000+i, len(message), message)
		if i == 0 {
			fmt.Fprintf(&history, "from %s\n", base)
		}
		fmt.Fprintf(&history, "M 100644 inline committed-%02d.txt\ndata %d\n%s\n", i, len(body), body)
	}
	history.WriteString("done\n")
	cmd := exec.CommandContext(worktreeTestContext(t), "git", "-C", wt, "fast-import", "--quiet")
	cmd.Stdin = strings.NewReader(history.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v: %s", err, out)
	}
	worktreeTestGit(t, wt, "reset", "--hard", ref)
	entries, err := worktreeGitJournalEntries(worktreeTestContext(t), wt, base)
	if err != nil {
		t.Fatal(err)
	}
	var commits, diffs []string
	for _, entry := range entries {
		switch entry.Kind {
		case "git-commit":
			commits = append(commits, entry.Message)
		case "git-diff":
			diffs = append(diffs, entry.Message)
		}
	}
	if len(commits) != worktreeJournalEntryLimit+1 || commits[len(commits)-1] != "...and 2 more commits" {
		t.Fatalf("capped commit entries = %#v", commits)
	}
	if len(diffs) != worktreeJournalEntryLimit+1 || diffs[len(diffs)-1] != "...and 2 more changed files" {
		t.Fatalf("capped diff entries = %#v", diffs)
	}
}

func initWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	values := settings.Defaults()
	if err := values.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := values.Save(); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	worktreeTestGit(t, repo, "init")
	worktreeTestGit(t, repo, "config", "user.name", "Dyna Test")
	worktreeTestGit(t, repo, "config", "user.email", "dyna-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, repo, "add", "base.txt")
	worktreeTestGit(t, repo, "commit", "-m", "base")
	return repo
}

func worktreeTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func worktreeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func removeWorktreeTestTree(t *testing.T, repo, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "remove", "--force", path).CombinedOutput()
}

// A schema that cannot be written as JSON fails the call. Before, it was
// dropped and the agent ran without validation.
func TestAgentSchemaThatIsNotJSONFailsTheCall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := &profile.Store{Profiles: []profile.Profile{{Name: "mock-schema", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 10}}}
	script := `return await agent("task", {schema: {type: "object", check: () => true}});`
	_, err := Execute(worktreeTestContext(t), Options{ScriptSrc: script, Store: store, WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "agent schema is not valid JSON") {
		t.Fatalf("Execute() error = %v, want the schema error", err)
	}
}
