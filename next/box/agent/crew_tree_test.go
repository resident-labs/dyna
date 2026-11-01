package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newTreeHarness gives a crew a real clone of a real bare repository, as a box has.
func newTreeHarness(t *testing.T) (*crewHarness, string) {
	t.Helper()
	h := newCrewHarness(t)
	t.Setenv("GIT_AUTHOR_NAME", "crew")
	t.Setenv("GIT_AUTHOR_EMAIL", "crew@dyna.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "crew")
	t.Setenv("GIT_COMMITTER_EMAIL", "crew@dyna.invalid")
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	gitIn(t, root, "init", "--bare", "-b", "main", origin)
	h.workdir = cloneOrigin(t, origin)
	writeFile(t, filepath.Join(h.workdir, "shared.txt"), "first\nsecond\nthird\n")
	gitIn(t, h.workdir, "add", "-A")
	gitIn(t, h.workdir, "commit", "-m", "start")
	gitIn(t, h.workdir, "push", "origin", "HEAD:main")
	h.branch = "v2/ses_1"
	h.act = func(dir, prompt string) {
		writeFile(t, filepath.Join(dir, strings.Fields(prompt)[0]+".txt"), prompt+"\n")
	}
	return h, origin
}

func cloneOrigin(t *testing.T, origin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	gitIn(t, filepath.Dir(dir), "clone", origin, dir)
	return dir
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestAWorktreeWorkerGetsItsOwnBranchAndItsWorkReachesTheRemote(t *testing.T) {
	h, origin := newTreeHarness(t)
	var mu sync.Mutex
	dirs := map[string]string{}
	h.act = func(dir, prompt string) {
		mu.Lock()
		dirs[prompt] = dir
		mu.Unlock()
		name := strings.Fields(prompt)[0]
		writeFile(t, filepath.Join(dir, name+".txt"), name)
		if name == "committer" {
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-m", "the worker's own commit")
			writeFile(t, filepath.Join(dir, "left-over.txt"), "uncommitted")
		}
	}
	got := h.result(crewMeta + `
		const [plain, committer, shared] = await parallel([
			() => agent('plain', { worktree: true }),
			() => agent('committer', { worktree: true, label: 'commits' }),
			() => agent('shared'),
		])
		return { plain, committer, shared }`).(map[string]any)

	plain := got["plain"].(map[string]any)
	if plain["text"] != "done: plain" || fmt.Sprint(plain["files"]) != "[plain.txt]" || plain["commit"] == "" {
		t.Fatalf("plain %v", plain)
	}
	committer := got["committer"].(map[string]any)
	if committer["step"] != "commits" || fmt.Sprint(committer["files"]) != "[committer.txt left-over.txt]" {
		t.Fatalf("committer %v", committer)
	}
	if got["shared"] != "done: shared" {
		t.Fatalf("a worker without a worktree keeps its plain result: %v", got["shared"])
	}
	branch := plain["branch"].(string)
	if !strings.HasPrefix(branch, "v2/ses_1-") || branch == committer["branch"] {
		t.Fatalf("branches %v and %v", branch, committer["branch"])
	}
	if remote := gitIn(t, origin, "rev-parse", "refs/heads/"+branch); remote != plain["commit"] {
		t.Fatalf("remote %s, result %v", remote, plain["commit"])
	}
	if shown := gitIn(t, origin, "show", committer["commit"].(string)+":left-over.txt"); shown != "uncommitted" {
		t.Fatalf("left-over.txt %q", shown)
	}
	if subjects := gitIn(t, origin, "log", "--format=%s", committer["commit"].(string)); !strings.Contains(subjects, "the worker's own commit") || !strings.Contains(subjects, "commits") {
		t.Fatalf("history %q", subjects)
	}
	// Each worker wrote its own tree. The main tree saw only the worker that shares it, and no tree stays behind.
	if dirs["plain"] == dirs["committer"] || dirs["plain"] == h.workdir || dirs["shared"] != h.workdir {
		t.Fatalf("trees %v", dirs)
	}
	if _, err := os.Stat(filepath.Join(h.workdir, "plain.txt")); !os.IsNotExist(err) {
		t.Fatal("a worktree worker's file reached the main tree")
	}
	if _, err := os.Stat(dirs["plain"]); !os.IsNotExist(err) {
		t.Fatal("a saved tree stayed behind")
	}
}

func TestASchemaWorktreeStepKeepsItsOutput(t *testing.T) {
	h, _ := newTreeHarness(t)
	h.reply = func(string, string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: `{"count": 3}`}, nil
	}
	got := h.result(crewMeta + `return await agent('counted', { worktree: true, schema: { type: 'object' } })`).(map[string]any)
	if fmt.Sprint(got["output"]) != "map[count:3]" || got["text"] != nil || fmt.Sprint(got["files"]) != "[counted.txt]" {
		t.Fatalf("result %v", got)
	}
}

func TestAWorkerThatChangesNothingStillGetsItsBranch(t *testing.T) {
	h, origin := newTreeHarness(t)
	h.act = nil
	got := h.result(crewMeta + `return await agent('idle', { worktree: true })`).(map[string]any)
	if fmt.Sprint(got["files"]) != "[]" || got["commit"] != gitIn(t, origin, "rev-parse", "main") {
		t.Fatalf("result %v", got)
	}
}

func TestAFailedPushFailsTheStepAndStoresNothingAsDone(t *testing.T) {
	h, _ := newTreeHarness(t)
	delay := pushDelay
	pushDelay = time.Millisecond
	t.Cleanup(func() { pushDelay = delay })
	gitIn(t, h.workdir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	got := h.result(crewMeta + `return await parallel([() => agent('lost', { worktree: true, label: 'lost' })])`).([]any)
	entry := got[0].(map[string]any)
	if entry["failed"] != true || !strings.Contains(entry["reason"].(string), "did not reach v2/ses_1-") {
		t.Fatalf("entry %v", entry)
	}
	for _, step := range h.journal.steps {
		if step.State != contract.WorkflowStepFailed || step.Result != "" {
			t.Fatalf("journal %+v", step)
		}
	}
}

func TestAFailedWorktreeWorkerSavesNothing(t *testing.T) {
	h, origin := newTreeHarness(t)
	h.reply = func(string, string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Outcome: contract.TurnFailed, Error: "the model refused"}, nil
	}
	got := h.result(crewMeta + `return await parallel([() => agent('refused', { worktree: true })])`).([]any)
	if got[0].(map[string]any)["failed"] != true {
		t.Fatalf("result %v", got)
	}
	if refs := gitIn(t, origin, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Fatalf("refs %q", refs)
	}
}

// The second box has a new clone at the pinned commit, as a replacement or a resumed run does.
func TestAReplacementBoxFindsStoredWorkOnTheRemote(t *testing.T) {
	h, origin := newTreeHarness(t)
	script := crewMeta + `
		const results = await parallel(['one', 'two'].map((name) => () => agent(name + ' work', { worktree: true, label: name })))
		const merged = await merge(results)
		const proof = await run(['git', 'show', results[0].commit + ':one.txt'])
		return { results, merged, proof: proof.stdout.trim() }`
	first := h.result(script).(map[string]any)
	if h.started.Load() != 2 {
		t.Fatalf("%d workers", h.started.Load())
	}
	h.base, h.workdir = gitIn(t, origin, "rev-parse", "main"), cloneOrigin(t, origin)
	second := h.result(script).(map[string]any)
	if h.started.Load() != 2 {
		t.Fatalf("the new box ran %d workers", h.started.Load())
	}
	if fmt.Sprint(second) != fmt.Sprint(first) || second["proof"] != "one work" {
		t.Fatalf("second %v, first %v", second, first)
	}
	branch := first["merged"].(map[string]any)["branch"].(string)
	if gitIn(t, h.workdir, "rev-parse", "refs/heads/"+branch) != gitIn(t, origin, "rev-parse", "refs/heads/"+branch) {
		t.Fatal("the new box did not fetch the run's branch")
	}
}

func TestStoredWorkThatIsGoneFailsLoudly(t *testing.T) {
	h, origin := newTreeHarness(t)
	script := crewMeta + `return await parallel([() => agent('one work', { worktree: true })])`
	first := h.result(script).([]any)[0].(map[string]any)
	gitIn(t, origin, "update-ref", "-d", "refs/heads/"+first["branch"].(string))
	h.workdir = cloneOrigin(t, origin)
	entry := h.result(script).([]any)[0].(map[string]any)
	if entry["failed"] != true || !strings.Contains(entry["reason"].(string), "did not come back") {
		t.Fatalf("entry %v", entry)
	}
	failed := 0
	for _, step := range h.journal.steps {
		if step.State == contract.WorkflowStepFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d failed entries: a loss must count at the run's end", failed)
	}
}

func TestMergeReportsAConflictAndKeepsTheOthers(t *testing.T) {
	h, origin := newTreeHarness(t)
	h.act = func(dir, prompt string) {
		if name, ok := strings.CutPrefix(prompt, "edit "); ok {
			writeFile(t, filepath.Join(dir, "shared.txt"), "first\n"+name+"\nthird\n")
			return
		}
		writeFile(t, filepath.Join(dir, prompt+".txt"), prompt)
	}
	script := crewMeta + `
		const results = await parallel([
			() => agent('edit left', { worktree: true, label: 'left' }),
			() => agent('edit right', { worktree: true, label: 'right' }),
			() => agent('alone', { worktree: true, label: 'alone' }),
			() => agent('another', { worktree: true, label: 'another' }),
		])
		return await merge(results)`
	got := h.result(script).(map[string]any)
	if fmt.Sprint(got["merged"]) != "[left alone another]" {
		t.Fatalf("merged %v", got)
	}
	conflicts := got["conflicts"].([]any)
	conflict := conflicts[0].(map[string]any)
	if len(conflicts) != 1 || conflict["step"] != "right" || fmt.Sprint(conflict["files"]) != "[shared.txt]" {
		t.Fatalf("conflicts %v", conflicts)
	}
	branch, commit := got["branch"].(string), got["commit"].(string)
	if branch != "v2/ses_1-run" || gitIn(t, origin, "rev-parse", "refs/heads/"+branch) != commit {
		t.Fatalf("branch %s at %s", branch, commit)
	}
	if text := gitIn(t, origin, "show", commit+":shared.txt"); text != "first\nleft\nthird" {
		t.Fatalf("shared.txt %q", text)
	}
	for _, file := range []string{"alone.txt", "another.txt"} {
		gitIn(t, origin, "cat-file", "-e", commit+":"+file)
	}
	if parents := gitIn(t, origin, "log", "--merges", "--format=%s", commit); strings.Count(parents, "merge ") != 3 || strings.Contains(parents, "right") {
		t.Fatalf("merges %q", parents)
	}

	names := func() []string {
		var failed []string
		h.journal.mu.Lock()
		defer h.journal.mu.Unlock()
		for key, step := range h.journal.steps {
			if step.State == contract.WorkflowStepFailed {
				failed = append(failed, h.journal.names[key]+": "+step.Reason)
			}
		}
		return failed
	}
	want := []string{"merge conflict: right: conflicts in shared.txt"}
	if got := names(); !slices.Equal(got, want) {
		t.Fatalf("failed entries %v", got)
	}
	// A resume keeps done steps and drops failed ones. The stored merge still counts its conflict.
	for key, step := range h.journal.steps {
		if step.State == contract.WorkflowStepFailed {
			delete(h.journal.steps, key)
		}
	}
	workers := h.started.Load()
	h.base, h.workdir = gitIn(t, origin, "rev-parse", "main"), cloneOrigin(t, origin)
	if again := h.result(script).(map[string]any); fmt.Sprint(again) != fmt.Sprint(got) || h.started.Load() != workers {
		t.Fatalf("resumed merge %v", again)
	}
	if got := names(); !slices.Equal(got, want) {
		t.Fatalf("failed entries after a resume %v", got)
	}
}

// What the guide tells a script to do with a conflict: give it to a worker, which merges by hand, and merge that on top.
func TestAWorkerCanRepairAConflictAndMergeBuildsOnIt(t *testing.T) {
	h, origin := newTreeHarness(t)
	h.act = func(dir, prompt string) {
		words := strings.Fields(prompt)
		if words[0] != "repair" {
			writeFile(t, filepath.Join(dir, "shared.txt"), "first\n"+words[1]+"\nthird\n")
			return
		}
		gitIn(t, dir, "fetch", "-q", "origin", words[1])
		gitIn(t, dir, "merge", "-q", "FETCH_HEAD")
		gitIn(t, dir, "fetch", "-q", "origin", words[2])
		if out, err := exec.Command("git", "-C", dir, "merge", "-q", "FETCH_HEAD").CombinedOutput(); err == nil {
			t.Errorf("the repair found no conflict: %s", out)
		}
		writeFile(t, filepath.Join(dir, "shared.txt"), "first\nboth\nthird\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-qm", "both")
	}
	got := h.result(crewMeta + `
		const steps = await parallel([() => agent('edit left', { worktree: true, label: 'left' }), () => agent('edit right', { worktree: true, label: 'right' })])
		const first = await merge(steps)
		const clash = first.conflicts[0]
		const repair = await agent('repair ' + first.branch + ' ' + clash.branch, { worktree: true, label: 'repair' })
		return await merge([repair], { base: first.commit })`).(map[string]any)
	if fmt.Sprint(got["conflicts"]) != "[]" || fmt.Sprint(got["merged"]) != "[repair]" {
		t.Fatalf("result %v", got)
	}
	if text := gitIn(t, origin, "show", got["commit"].(string)+":shared.txt"); text != "first\nboth\nthird" {
		t.Fatalf("shared.txt %q", text)
	}
}

func TestMergeBuildsOnABaseAndPassesOverAFailedStep(t *testing.T) {
	h, origin := newTreeHarness(t)
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		if prompt == "bad work" {
			return contract.TurnEnded{Outcome: contract.TurnFailed, Error: "the model refused"}, nil
		}
		return contract.TurnEnded{Final: prompt}, nil
	}
	got := h.result(crewMeta + `
		const [one, bad] = await parallel([
			() => agent('one work', { worktree: true, label: 'one' }),
			() => agent('bad work', { worktree: true, label: 'bad' }),
		])
		const first = await merge([one, bad])
		const two = await agent('two work', { worktree: true, label: 'two' })
		return { first, second: await merge([two], { base: first.commit, label: 'on top' }) }`).(map[string]any)
	first := got["first"].(map[string]any)
	conflict := first["conflicts"].([]any)[0].(map[string]any)
	if fmt.Sprint(first["merged"]) != "[one]" || conflict["step"] != "bad" || conflict["reason"] != "the model refused" {
		t.Fatalf("first %v", first)
	}
	commit := got["second"].(map[string]any)["commit"].(string)
	for _, file := range []string{"one.txt", "two.txt"} {
		gitIn(t, origin, "cat-file", "-e", commit+":"+file)
	}
}

func TestMergeThrowsOnlyWhenItIsMisused(t *testing.T) {
	h, _ := newTreeHarness(t)
	got := h.result(crewMeta + `
		const plain = await agent('plain')
		return [await merge([plain]).catch((e) => e.reason), await merge('text').catch((e) => e.reason), await merge([], { nope: 1 }).catch((e) => e.reason)]`).([]any)
	for i, want := range []string{"worktree: true", "worktree: true", "unknown field"} {
		if !strings.Contains(fmt.Sprint(got[i]), want) {
			t.Fatalf("%d: %v", i, got[i])
		}
	}
}

func TestWorktreeNeedsARepository(t *testing.T) {
	h := newCrewHarness(t)
	got := h.result(crewMeta + `return await parallel([() => agent('x', { worktree: true }), () => merge([])])`).([]any)
	for _, entry := range got {
		if !strings.Contains(fmt.Sprint(entry), "this run has none") {
			t.Fatalf("result %v", got)
		}
	}
}

// lockedCrew has a crew's shims ahead of a fake tool folder, as a box has after Export.
func lockedCrew(t *testing.T, tools map[string]string) (*Crew, string) {
	t.Helper()
	folder := t.TempDir()
	for name, script := range tools {
		writeFile(t, filepath.Join(folder, name), "#!/bin/bash\n"+script+"\n")
	}
	t.Setenv("PATH", folder+":"+os.Getenv("PATH"))
	t.Setenv("CARGO_TARGET_DIR", "")
	state, sessions := t.TempDir(), t.TempDir()
	crew, err := NewCrew(CrewConfig{Workflow: contract.BoxWorkflow{Workers: 4}, Engine: newFakeJournal(), Transcript: NewTranscript(&transcriptEngine{}, sessions), Workdir: t.TempDir(), Sessions: sessions, State: state})
	if err != nil {
		t.Fatal(err)
	}
	crew.seen = make(map[string]int)
	t.Cleanup(func() { _ = crew.Close() })
	if err := crew.Export(); err != nil {
		t.Fatal(err)
	}
	return crew, folder
}

func inShell(t *testing.T, ctx context.Context, command string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.WaitDelay = 100 * time.Millisecond // a shim killed in the queue may leave a child on the pipe
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBuildsTakeTurnsAndReadsDoNotWait(t *testing.T) {
	marks := t.TempDir()
	crew, _ := lockedCrew(t, map[string]string{
		"make":  `mkdir "` + marks + `/building" || { echo overlap; exit 9; }; sleep 0.3; rmdir "` + marks + `/building"`,
		"go":    `echo "real go: $*"`,
		"cargo": `echo "real cargo: $*"`,
		"npm":   `echo "real npm: $*"`,
	})
	if got := os.Getenv("CARGO_TARGET_DIR"); got != filepath.Join(crew.config.Workdir, "target") {
		t.Fatalf("CARGO_TARGET_DIR %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	release, err := crew.builds.acquire(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var builds sync.WaitGroup
	for range 3 {
		builds.Go(func() {
			if out, err := inShell(t, ctx, "make"); err != nil {
				t.Errorf("make: %v: %s", err, out)
			}
		})
	}
	// run() takes the same lock, in-process.
	builds.Go(func() {
		reply := crew.commandCall(ctx, crewCall{Op: "run", Payload: []byte(`"mkdir ` + marks + `/building && sleep 0.1 && rmdir ` + marks + `/building"`), Opts: []byte(`{}`)})
		if reply.Error != "" {
			t.Errorf("run: %s", reply.Error)
		}
	})
	for crew.builds.waited() == 0 {
		time.Sleep(time.Millisecond)
	}
	for _, read := range []string{"go version", "go env GOPATH", "cargo metadata", "npm ls", "npm --version", "cargo --version", "grep -c . /dev/null || true", "git --version"} {
		started := time.Now()
		out, err := inShell(t, ctx, read)
		if err != nil || time.Since(started) > 2*time.Second {
			t.Fatalf("%q held for %s: %v: %s", read, time.Since(started), err, out)
		}
	}
	release()
	builds.Wait()
	if _, err := os.Stat(filepath.Join(marks, "building")); !os.IsNotExist(err) {
		t.Fatal("a build was left in the middle")
	}
}

func TestAShimPassesThroughAndABuildFindsTheRealTools(t *testing.T) {
	crew, _ := lockedCrew(t, map[string]string{
		"go":   `echo "go $*"; exit 7`,
		"make": `go test ./...; echo "inner exit $?"; [ "$1" != quiet ] || exit 5`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A build runs inside the lock, so a tool it calls must not queue behind it. This would hang otherwise.
	if out, err := inShell(t, ctx, "make"); err != nil || out != "go test ./...\ninner exit 7\n" {
		t.Fatalf("%q: %v", out, err)
	}
	if out, err := inShell(t, ctx, "make quiet >/dev/null"); err == nil || !strings.Contains(err.Error(), "exit status 5") {
		t.Fatalf("%q: %v", out, err)
	}
	if out, err := inShell(t, ctx, "go build ./... || echo code $?"); err != nil || !strings.Contains(out, "code 7") {
		t.Fatalf("%q: %v", out, err)
	}
	// run() holds the lock itself, so what it runs must not queue behind it either.
	if reply := crew.commandCall(ctx, crewCall{Op: "run", Payload: []byte(`"make"`), Opts: []byte(`{}`)}); reply.Error != "" {
		t.Fatalf("run: %s", reply.Error)
	}
	// A wrapper the shim passes keeps the shims for what it runs, so its builds still queue.
	release, err := crew.builds.acquire(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waiting, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stop()
	if out, err := inShell(t, waiting, "go version; make"); err == nil {
		t.Fatalf("a build ran past the lock: %q", out)
	}
}

// Real cargo gives every worktree's crate one name in a shared target and judges it by file times, so a tree whose files are
// older than the last build would get that build's binary. This cargo keeps that rule.
func TestEachTreeBuildsItsOwnCodeInASharedTarget(t *testing.T) {
	crew, _ := lockedCrew(t, map[string]string{"cargo": `
source=$(git rev-parse --show-toplevel)/code
if [ "$source" -nt "$CARGO_TARGET_DIR/binary" ]; then cp "$source" "$CARGO_TARGET_DIR/binary"; fi
cat "$CARGO_TARGET_DIR/binary"`})
	if err := os.MkdirAll(os.Getenv("CARGO_TARGET_DIR"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tree := func(name string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "init", "-q")
		writeFile(t, filepath.Join(dir, "code"), name)
		gitIn(t, dir, "add", "-A")
		return dir
	}
	one, two := tree("one"), tree("two")
	time.Sleep(20 * time.Millisecond) // Both trees' files are now older than anything built from here on.
	build := func(dir string) string {
		t.Helper()
		out, err := inShell(t, ctx, "cd "+dir+" && cargo build")
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return out
	}
	for i, step := range [][2]string{{one, "one"}, {two, "two"}, {two, "two"}, {one, "one"}} {
		if got := build(step[0]); got != step[1] {
			t.Fatalf("build %d in %s gave %q", i, step[1], got)
		}
	}
	// run() in another tree takes the same path, with the lock it already holds.
	reply := crew.commandCall(ctx, crewCall{Op: "run", Payload: []byte(`"cargo build"`), Opts: []byte(`{"cwd":"` + two + `"}`)})
	if reply.Error != "" || reply.Value.(commandResult).Stdout != "two" {
		t.Fatalf("run gave %+v", reply)
	}
}

func TestAWaitingBuildSaysSo(t *testing.T) {
	crew, _ := lockedCrew(t, map[string]string{"make": `echo built`})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	release, err := crew.builds.acquire(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, release)
	out, err := inShell(t, ctx, "make")
	if err != nil || !strings.Contains(out, "waiting for the box's build lock") || !strings.Contains(out, "built") {
		t.Fatalf("%q: %v", out, err)
	}
}

func TestABuildThatNeverEndsLetsTheNextOneGo(t *testing.T) {
	lock, err := newBuildLock(t.TempDir(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.listener.Close()
	ask := func() *bufio.Reader {
		conn, err := net.Dial("tcp", lock.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		return bufio.NewReader(conn)
	}
	if line, err := ask().ReadString('\n'); err != nil || line != "go\n" {
		t.Fatalf("%q: %v", line, err)
	}
	// The first holder never hangs up. The next one waits out the limit and goes.
	next := ask()
	for _, want := range []string{"wait\n", "go\n"} {
		if line, err := next.ReadString('\n'); err != nil || line != want {
			t.Fatalf("%q, want %q: %v", line, want, err)
		}
	}
}

func TestALockWaitDoesNotCountAgainstAWorkersTimeout(t *testing.T) {
	lock, err := newBuildLock(t.TempDir(), buildLockHold)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.listener.Close()
	ctx := context.Background()
	release, err := lock.acquire(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	work, cancel := lock.timeout(ctx, 300*time.Millisecond)
	defer cancel()
	queued := make(chan struct{})
	go func() {
		next, err := lock.acquire(ctx, func() { close(queued) })
		if err == nil {
			time.Sleep(time.Millisecond)
			next()
		}
	}()
	<-queued
	time.Sleep(800 * time.Millisecond) // well past the limit, all of it in the queue
	if work.Err() != nil {
		t.Fatal("a worker timed out in the queue")
	}
	release()
	select {
	case <-work.Done():
		if context.Cause(work) != context.DeadlineExceeded {
			t.Fatalf("cause %v", context.Cause(work))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timeout never came back once the queue was gone")
	}
}

func TestAWorkersTimeoutStillEndsIt(t *testing.T) {
	h := newCrewHarness(t)
	h.reply = func(string, string) (contract.TurnEnded, error) {
		time.Sleep(1500 * time.Millisecond)
		return contract.TurnEnded{Final: "too late"}, nil
	}
	got := h.result(crewMeta + `return await parallel([() => agent('slow', { timeout: 1 })])`).([]any)
	if reason := got[0].(map[string]any)["reason"]; reason != "timed out after 1s" {
		t.Fatalf("result %v", got)
	}
}
