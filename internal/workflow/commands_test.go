package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

// fakeCommands runs no process. Each command ends with the exit its script of
// outcomes gives, and Wait blocks on hold when a test sets it.
type fakeCommands struct {
	mu       sync.Mutex
	specs    []CommandSpec
	attached []string
	stopped  []string
	exits    map[string]CommandExit
	stdout   map[string]string
	active   int
	peak     int
	hold     chan struct{}
	started  chan string
	next     func(spec CommandSpec) (CommandExit, string)
}

func newFakeCommands() *fakeCommands {
	return &fakeCommands{exits: map[string]CommandExit{}, stdout: map[string]string{}}
}

func (f *fakeCommands) Start(_ context.Context, spec CommandSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	id := fmt.Sprintf("sh_%d", len(f.specs))
	exit, out := CommandExit{LogPath: "/logs/" + id}, ""
	if f.next != nil {
		exit, out = f.next(spec)
		exit.LogPath = "/logs/" + id
	}
	f.exits[id], f.stdout[id] = exit, out
	f.active++
	f.peak = max(f.peak, f.active)
	if f.started != nil {
		f.started <- id
	}
	return id, nil
}

func (f *fakeCommands) Attach(_ context.Context, shell string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, shell)
	if _, ok := f.exits[shell]; !ok {
		return fmt.Errorf("unknown shell %s", shell)
	}
	return nil
}

func (f *fakeCommands) Wait(ctx context.Context, shell string) (CommandExit, error) {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return CommandExit{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold != nil || f.started != nil {
		f.active--
	}
	return f.exits[shell], nil
}

func (f *fakeCommands) Output(shell string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stdout[shell], nil
}

func (f *fakeCommands) Stop(shell string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, shell)
	return nil
}

func runCommandScript(t *testing.T, script string, opts Options) (string, []runstore.Event, error) {
	t.Helper()
	var rec workflowEventRecorder
	opts.Store, opts.WorkDir, opts.OnEvent = &profile.Store{}, t.TempDir(), rec.append
	if opts.ScriptPath == "" {
		opts.ScriptPath = "cmd.js"
	}
	result, err := executeWorkflowTest(t, script, opts)
	return result, rec.snapshot(), err
}

func TestRunReturnsParsedJSONFromAnArgumentArray(t *testing.T) {
	fake := newFakeCommands()
	fake.next = func(CommandSpec) (CommandExit, string) { return CommandExit{}, `{"ok":true}` + "\n" }
	result, events, err := runCommandScript(t, `
const r = await run(['echo', '{"ok":true}'], { parse: 'json', label: 'echo-ok', lane: 'a', stage: 'build' })
return { ok: r.json.ok, code: r.code, shell: r.shellId, log: r.logPath, attempts: r.attempts }
`, Options{Commands: fake})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if want := `{"ok":true,"code":0,"shell":"sh_1","log":"/logs/sh_1","attempts":1}`; result != want {
		t.Fatalf("result = %s, want %s", result, want)
	}
	if got := fake.specs[0].Argv; len(got) != 2 || got[0] != "echo" {
		t.Fatalf("argv = %q, want the array run without a shell", got)
	}
	var start, end runstore.Event
	for _, ev := range events {
		switch ev.T {
		case "exec_start":
			start = ev
		case "exec_end":
			end = ev
		}
	}
	if start.Label != "echo-ok" || start.Lane != "a" || start.Stage != "build" || !strings.Contains(start.Command, "echo") {
		t.Fatalf("exec_start = %+v, want the label, lane, stage and command", start)
	}
	if end.Status != "ok" || end.ExitCode == nil || *end.ExitCode != 0 || end.Shell != "sh_1" || end.ID != start.ID {
		t.Fatalf("exec_end = %+v, want ok with exit code 0 and the shell", end)
	}
}

func TestRunStringCommandUsesTheShell(t *testing.T) {
	fake := newFakeCommands()
	_, _, err := runCommandScript(t, `await run('echo hi | cat')`, Options{Commands: fake})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := strings.Join(fake.specs[0].Argv, "|"); got != "sh|-c|echo hi | cat" {
		t.Fatalf("argv = %q, want sh -c with the string", got)
	}
}

func TestRunFailureRejectsUnlessCheckIsFalse(t *testing.T) {
	fake := newFakeCommands()
	fake.next = func(CommandSpec) (CommandExit, string) { return CommandExit{Code: 3}, "partial" }
	result, events, err := runCommandScript(t, `
const [dropped, kept] = await parallel([
  () => run('exit 3', { label: 'strict' }),
  () => run('exit 3', { check: false, label: 'lenient' }),
])
return { dropped, code: kept.code, stdout: kept.stdout }
`, Options{Commands: fake})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if want := `{"dropped":null,"code":3,"stdout":"partial"}`; result != want {
		t.Fatalf("result = %s, want %s", result, want)
	}
	var failed int
	for _, ev := range events {
		if ev.T == "exec_end" && ev.Status == "error" && ev.ExitCode != nil && *ev.ExitCode == 3 {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("%d exec_end events report exit 3, want 2 (check: false still marks the step failed)", failed)
	}
}

func TestRunRejectsOutputThatIsNotJSON(t *testing.T) {
	fake := newFakeCommands()
	fake.next = func(CommandSpec) (CommandExit, string) { return CommandExit{}, "not json" }
	_, _, err := runCommandScript(t, `await run('echo x', { parse: 'json' })`, Options{Commands: fake})
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("error = %v, want a JSON parse failure", err)
	}
}

func TestRunWithoutARunnerFails(t *testing.T) {
	_, _, err := runCommandScript(t, `await run('true')`, Options{})
	if err == nil || !strings.Contains(err.Error(), "cannot run commands") {
		t.Fatalf("error = %v, want a refusal", err)
	}
}

func TestRunCapsCommandsThatRunAtOnce(t *testing.T) {
	fake := newFakeCommands()
	fake.hold, fake.started = make(chan struct{}), make(chan string, 6)
	done := make(chan error, 1)
	go func() {
		_, _, err := runCommandScript(t, `
await parallel([1, 2, 3, 4, 5, 6].map((n) => () => run(['true'], { label: 'job-' + n })))
`, Options{Commands: fake, MaxCommands: 4})
		done <- err
	}()
	for range 4 {
		<-fake.started
	}
	close(fake.hold)
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if fake.peak != 4 || len(fake.specs) != 6 {
		t.Fatalf("peak = %d with %d commands, want a cap of 4 over 6 commands", fake.peak, len(fake.specs))
	}
}

func TestRunDoesNotCountAgainstTheAgentCap(t *testing.T) {
	fake := newFakeCommands()
	_, _, err := runCommandScript(t, `
for (let i = 0; i < 3; i++) await run('true', { label: 'c' + i })
`, Options{Commands: fake, MaxAgents: 1})
	if err != nil {
		t.Fatalf("Execute() error = %v, want commands outside the agent cap", err)
	}
}

func TestRunResumeReusesFinishedAndReattachesRunning(t *testing.T) {
	cwd := t.TempDir()
	argv := []string{"sh", "-c", "build"}
	doneKey := commandKey(argv, cwd, "done-step", "", nil)
	liveKey := commandKey([]string{"sh", "-c", "wait"}, cwd, "live-step", "", nil)
	failedKey := commandKey([]string{"sh", "-c", "flaky"}, cwd, "failed-step", "", nil)
	cache := NewCache([]runstore.JournalEntry{
		{ID: 1, Key: doneKey, Kind: "exec", State: "running", Shell: "sh_old1"},
		{ID: 1, Key: doneKey, Kind: "exec", State: "done", Shell: "sh_old1", Result: map[string]any{"code": float64(0), "stdout": "kept"}},
		{ID: 2, Key: liveKey, Kind: "exec", State: "running", Shell: "sh_old2"},
		{ID: 3, Key: failedKey, Kind: "exec", State: "error", Shell: "sh_old3", Error: "exit 1"},
	})
	fake := newFakeCommands()
	fake.exits["sh_old2"], fake.stdout["sh_old2"] = CommandExit{}, "from the live shell"
	result, events, err := runCommandScript(t, `
const a = await run('build', { label: 'done-step', cwd: args.cwd })
const b = await run('wait', { label: 'live-step', cwd: args.cwd })
const c = await run('flaky', { label: 'failed-step', cwd: args.cwd })
return { a: a.stdout, b: b.stdout, c: c.shellId }
`, Options{Commands: fake, Cache: cache, Args: map[string]any{"cwd": cwd}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if want := `{"a":"kept","b":"from the live shell","c":"sh_1"}`; result != want {
		t.Fatalf("result = %s, want %s", result, want)
	}
	if len(fake.specs) != 1 || fake.specs[0].Argv[2] != "flaky" {
		t.Fatalf("started %d commands, want only the failed one to run again", len(fake.specs))
	}
	if len(fake.attached) != 1 || fake.attached[0] != "sh_old2" {
		t.Fatalf("attached = %v, want only the live shell", fake.attached)
	}
	cached := 0
	for _, ev := range events {
		if ev.T == "exec_end" && ev.Cached {
			cached++
		}
	}
	if cached != 1 {
		t.Fatalf("%d cached exec_end events, want 1 (the finished command)", cached)
	}
}

func TestCacheKeepsOneRecordPerExecCall(t *testing.T) {
	key := commandKey([]string{"a"}, "/w", "x", "", nil)
	dup := commandKey([]string{"b"}, "/w", "x", "", nil)
	cache := NewCache([]runstore.JournalEntry{
		{ID: 1, Key: key, Kind: "exec", State: "running", Shell: "sh_1"},
		{ID: 1, Key: key, Kind: "exec", State: "done", Shell: "sh_1", Result: "r"},
		{ID: 2, Key: dup, Kind: "exec", State: "done", Shell: "sh_2", Result: "x"},
		{ID: 3, Key: dup, Kind: "exec", State: "done", Shell: "sh_3", Result: "y"},
	})
	if stats := cache.Stats(); stats.PriorCalls != 3 {
		t.Fatalf("PriorCalls = %d, want 3 calls, not 4 journal lines", stats.PriorCalls)
	}
	if rec, ok := cache.popExec(key); !ok || !rec.Done || rec.Result != "r" {
		t.Fatalf("popExec(key) = %+v, %t, want the finished result", rec, ok)
	}
	if _, ok := cache.popExec(dup); ok {
		t.Fatal("two calls share a key, so neither is reused")
	}
}

func TestRunEnvIsPassedToTheRunner(t *testing.T) {
	fake := newFakeCommands()
	_, _, err := runCommandScript(t, `await run('env', { env: { B: '2', A: '1' } })`, Options{Commands: fake})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	b, _ := json.Marshal(fake.specs[0].Env)
	if string(b) != `["A=1","B=2"]` {
		t.Fatalf("env = %s, want sorted pairs", b)
	}
}
