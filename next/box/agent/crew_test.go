package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// fakeJournal is the engine's side of a crew, kept in memory so that a second Crew can resume from it.
type fakeJournal struct {
	mu    sync.Mutex
	steps map[string]contract.WorkflowStepEndRequest
	names map[string]string
	files map[string]string // what each running worker reported
	lanes map[string]string
	posts []contract.PostRequest
	lines []string // status() lines
	pin   string
	// by step name, the script version that started each step
	versions map[string]int
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{steps: map[string]contract.WorkflowStepEndRequest{}, names: map[string]string{}, files: map[string]string{}, lanes: map[string]string{}}
}

func (j *fakeJournal) WorkflowSetStatus(_ context.Context, req contract.WorkflowSetStatusRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, req.Text)
	return nil
}

func (j *fakeJournal) WorkflowPin(_ context.Context, commit string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.pin == "" {
		j.pin = commit
	}
	return nil
}

func (j *fakeJournal) WorkflowStepStart(_ context.Context, req contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.names[req.Key] = req.Name
	j.lanes[req.Key] = req.Lane
	if stored, ok := j.steps[req.Key]; ok && req.Kind != contract.WorkflowStepWorkflow {
		return contract.WorkflowStepStartResponse{State: stored.State, Result: stored.Result, Reason: stored.Reason}, nil
	}
	if j.versions == nil {
		j.versions = map[string]int{}
	}
	j.versions[req.Name] = req.Version
	return contract.WorkflowStepStartResponse{}, nil
}

func (j *fakeJournal) WorkflowStepEnd(_ context.Context, req contract.WorkflowStepEndRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps[req.Key] = req
	return nil
}

func (j *fakeJournal) WorkflowStepTranscript(_ context.Context, req contract.WorkflowStepTranscriptRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.files[req.Key] = req.Transcript
	return nil
}

func (j *fakeJournal) WorkflowPost(_ context.Context, req contract.PostRequest) (contract.PostResponse, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.posts = append(j.posts, req)
	return contract.PostResponse{Event: "ev_1", Seq: int64(len(j.posts))}, nil
}

// fakeWorker answers as reply says. It stands in for a Pi process.
type fakeWorker struct {
	reply func(prompt string, model string) (contract.TurnEnded, error)
	model string
	file  string
	act   func(dir, prompt string) // what the worker does to its tree before it answers
	hold  func(ctx context.Context, prompt string) error
	dir   string
}

func (w *fakeWorker) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, _ func()) (contract.TurnEnded, error) {
	if w.act != nil {
		w.act(w.dir, inputs[0].Text)
	}
	if w.hold != nil {
		if err := w.hold(ctx, inputs[0].Text); err != nil {
			return contract.TurnEnded{}, err
		}
	}
	ended, err := w.reply(inputs[0].Text, w.model)
	if ended.Outcome == "" {
		ended.Outcome = contract.TurnCompleted
	}
	ended.Usage.InputTokens, ended.Turn = 10, id
	return ended, err
}
func (w *fakeWorker) SessionFile() string { return w.file }
func (w *fakeWorker) Close() error        { return nil }

type crewHarness struct {
	t       *testing.T
	journal *fakeJournal
	started atomic.Int32
	reply   func(prompt, model string) (contract.TurnEnded, error)
	act     func(dir, prompt string)
	hold    func(ctx context.Context, prompt string) error
	workdir string
	branch  string
	base    string
}

func newCrewHarness(t *testing.T) *crewHarness {
	t.Helper()
	return &crewHarness{t: t, journal: newFakeJournal(), workdir: t.TempDir(), reply: func(prompt, _ string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: "done: " + prompt}, nil
	}}
}

// run executes a script in a new Crew, as a new box would, and returns the turn.
func (h *crewHarness) run(script string, profiles ...contract.WorkflowProfile) contract.TurnEnded {
	h.t.Helper()
	_, ended := h.start(script, profiles...)
	return <-ended
}

var crewProfiles = []contract.WorkflowProfile{{Name: "fast", Model: contract.ModelChoice{ID: "fast-model"}}, {Name: "deep", Model: contract.ModelChoice{ID: "deep-model"}}}

// start runs a script in a new Crew, whose turn comes back on the channel.
func (h *crewHarness) start(script string, profiles ...contract.WorkflowProfile) (*Crew, <-chan contract.TurnEnded) {
	h.t.Helper()
	if profiles == nil {
		profiles = crewProfiles
	}
	state, sessions := h.t.TempDir(), h.t.TempDir()
	var opened atomic.Int32
	crew, err := NewCrew(CrewConfig{
		Workflow: contract.BoxWorkflow{Run: "wf_1", Name: "test", Script: script, Version: 1, Args: `{"topic":"bounds"}`, Profiles: profiles, Workers: 16},
		Engine:   h.journal,
		Open: func(_ context.Context, profile contract.WorkflowProfile, folder, dir string) (crewWorker, error) {
			data, err := os.ReadFile(filepath.Join(folder, "models.json"))
			if err != nil || !strings.Contains(string(data), contract.WorkerHeader) {
				h.t.Errorf("worker configuration %s: %v", data, err)
			}
			h.started.Add(1)
			return &fakeWorker{reply: h.reply, act: h.act, hold: h.hold, dir: dir, model: profile.Model.ID, file: filepath.Join(sessions, fmt.Sprintf("w%d.jsonl", opened.Add(1)))}, nil
		},
		Transcript: NewTranscript(&transcriptEngine{}, sessions),
		Workdir:    h.workdir,
		Branch:     h.branch,
		Base:       h.base,
		Sessions:   sessions,
		State:      state,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	out := make(chan contract.TurnEnded, 1)
	go func() {
		defer cancel()
		ended, err := crew.Start(ctx, "turn_1", []contract.TurnInput{{Delivery: 7, Text: "run it"}}, func() {})
		if err != nil {
			h.t.Error(err)
		}
		out <- ended
	}()
	return crew, out
}

func (h *crewHarness) result(script string) any {
	h.t.Helper()
	ended := h.run(script)
	if ended.Outcome != contract.TurnCompleted {
		h.t.Fatalf("turn %+v", ended)
	}
	var value any
	if err := json.Unmarshal([]byte(ended.Final), &value); err != nil {
		return ended.Final
	}
	return value
}

const crewMeta = "export const meta = { name: 'test', profiles: ['fast', 'deep'] }\n"

func TestScriptFansOutAndReturnsItsResult(t *testing.T) {
	h := newCrewHarness(t)
	got := h.result(crewMeta + `
		const texts = await parallel([
			() => agent('one ' + args.topic),
			() => agent('two', { profile: 'deep' }),
		])
		const piped = await pipeline(['a', 'b'], (item) => agent('first ' + item), (previous, item, i) => previous + ' then ' + item + i)
		return { texts, piped, name: meta.name }`)
	want := map[string]any{"texts": []any{"done: one bounds", "done: two"}, "piped": []any{"done: first a then a0", "done: first b then b1"}, "name": "test"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("result %v", got)
	}
	if h.started.Load() != 4 {
		t.Fatalf("%d workers", h.started.Load())
	}
}

func TestAResumedScriptReusesStepsAndRerunsAnEditedOne(t *testing.T) {
	h := newCrewHarness(t)
	script := crewMeta + `return await parallel(['a', 'b', 'c'].map((n) => () => agent('audit ' + n)))`
	first := h.result(script)
	if h.started.Load() != 3 {
		t.Fatalf("%d workers", h.started.Load())
	}
	if again := h.result(script); fmt.Sprint(again) != fmt.Sprint(first) || h.started.Load() != 3 {
		t.Fatalf("resume ran workers: %d, %v", h.started.Load(), again)
	}
	// The same steps in another order still hit, because no key depends on call order.
	h.result(crewMeta + `return await parallel(['c', 'a', 'b'].map((n) => () => agent('audit ' + n)))`)
	if h.started.Load() != 3 {
		t.Fatalf("a reorder ran workers: %d", h.started.Load())
	}
	h.result(crewMeta + `return await parallel(['a', 'B', 'c'].map((n) => () => agent('audit ' + n)))`)
	if h.started.Load() != 4 {
		t.Fatalf("an edit ran %d workers, want one more", h.started.Load())
	}
}

func TestTrueDuplicatesEachRun(t *testing.T) {
	h := newCrewHarness(t)
	var calls atomic.Int32
	h.reply = func(string, string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: fmt.Sprint(calls.Add(1))}, nil
	}
	script := crewMeta + `return [await agent('same'), await agent('same')]`
	first := h.result(script)
	if calls.Load() != 2 {
		t.Fatalf("%d calls", calls.Load())
	}
	if again := h.result(script); fmt.Sprint(again) != fmt.Sprint(first) || calls.Load() != 2 {
		t.Fatalf("%v after %d calls", again, calls.Load())
	}
}

// heldTasks keeps each worker whose prompt names a held task at work until the task is released, and counts them.
type heldTasks struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	working map[string]int
	stopped map[string]int // workers whose step ended before their task was released
}

func newHeldTasks(tasks ...string) *heldTasks {
	held := &heldTasks{gates: map[string]chan struct{}{}, working: map[string]int{}, stopped: map[string]int{}}
	for _, task := range tasks {
		held.gates[task] = make(chan struct{})
	}
	return held
}

func (h *heldTasks) hold(ctx context.Context, prompt string) error {
	h.mu.Lock()
	task := strings.TrimPrefix(prompt, "work on ")
	gate := h.gates[task]
	if gate != nil {
		h.working[task]++
	}
	h.mu.Unlock()
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		h.mu.Lock()
		h.stopped[task]++
		h.mu.Unlock()
		return ctx.Err()
	}
}

func (h *heldTasks) count(of map[string]int, task string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return of[task]
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}
	}
}

func reloaded(script string, version int, profiles ...contract.WorkflowProfile) contract.BoxWorkflow {
	if profiles == nil {
		profiles = crewProfiles
	}
	return contract.BoxWorkflow{Run: "wf_1", Name: "test", Script: script, Version: version, Args: `{"topic":"bounds"}`, Profiles: profiles, Workers: 16}
}

// A reload runs the new script in the same turn. A finished step returns its stored result, a running one is joined and its
// worker's reply comes back to the new script, and a new key starts a worker. Each step keeps the version that started it.
func TestAReloadJoinsRunningStepsAndKeepsFinishedOnes(t *testing.T) {
	h := newCrewHarness(t)
	held := newHeldTasks("slow")
	h.hold = held.hold
	crew, ended := h.start(crewMeta + `return await parallel([() => agent('work on quick'), () => agent('work on slow')])`)
	waitFor(t, "the quick step stored and the slow one working", func() bool {
		h.journal.mu.Lock()
		stored := len(h.journal.steps)
		h.journal.mu.Unlock()
		return stored == 1 && held.count(held.working, "slow") == 1
	})
	crew.Reload(reloaded(crewMeta+`return await parallel(['quick', 'slow', 'new'].map((task) => () => agent('work on ' + task)))`, 2))
	waitFor(t, "the new step's worker", func() bool { return h.started.Load() == 3 })
	time.Sleep(200 * time.Millisecond)
	if got := held.count(held.working, "slow"); got != 1 || h.started.Load() != 3 {
		t.Fatalf("%d slow workers and %d in all: the reload started a step again", got, h.started.Load())
	}
	close(held.gates["slow"])
	got := <-ended
	if got.Outcome != contract.TurnCompleted || got.Final != `["done: work on quick","done: work on slow","done: work on new"]` {
		t.Fatalf("turn %+v", got)
	}
	if h.started.Load() != 3 || held.count(held.stopped, "slow") != 0 {
		t.Fatalf("%d workers", h.started.Load())
	}
	if want := map[string]int{"agent: work on quick": 1, "agent: work on slow": 1, "agent: work on new": 2}; fmt.Sprint(h.journal.versions) != fmt.Sprint(want) {
		t.Fatalf("versions %v", h.journal.versions)
	}
	// An older or equal version changes nothing.
	crew.Reload(reloaded(crewMeta+`return 'old'`, 2))
	if crew.workflow.Script == crewMeta+`return 'old'` {
		t.Fatal("a reload to the version the crew runs replaced its script")
	}
}

// A running step the new script never calls goes on until the run ends, and then stops and stores nothing.
func TestAReloadLeavesAStepItDropsRunningUntilTheEnd(t *testing.T) {
	h := newCrewHarness(t)
	held := newHeldTasks("slow", "last")
	h.hold = held.hold
	crew, ended := h.start(crewMeta + `return await agent('work on slow')`)
	waitFor(t, "the slow worker", func() bool { return held.count(held.working, "slow") == 1 })
	crew.Reload(reloaded(crewMeta+`return await agent('work on last')`, 2))
	waitFor(t, "the last worker", func() bool { return held.count(held.working, "last") == 1 })
	if held.count(held.stopped, "slow") != 0 {
		t.Fatal("the reload stopped a running step")
	}
	close(held.gates["last"])
	if got := <-ended; got.Outcome != contract.TurnCompleted || got.Final != "done: work on last" {
		t.Fatalf("turn %+v", got)
	}
	if held.count(held.stopped, "slow") != 1 {
		t.Fatal("the dropped step outlived the run")
	}
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	if len(h.journal.steps) != 1 {
		t.Fatalf("journal %v: a step the end cut off stores nothing", h.journal.steps)
	}
}

// The new version's profiles replace the old ones: its default is its own first profile, and a profile it left out fails.
func TestAReloadSwitchesProfiles(t *testing.T) {
	h := newCrewHarness(t)
	h.reply = func(prompt, model string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: prompt + " on " + model}, nil
	}
	held := newHeldTasks("slow")
	h.hold = held.hold
	fast, deep := crewProfiles[:1], []contract.WorkflowProfile{crewProfiles[1]}
	crew, ended := h.start("export const meta = { name: 'test', profiles: ['fast'] }\n"+`return await agent('work on slow')`, fast...)
	waitFor(t, "the slow worker", func() bool { return held.count(held.working, "slow") == 1 })
	crew.Reload(reloaded("export const meta = { name: 'test', profiles: ['deep'] }\n"+`return [await agent('x'), await agent('y', { profile: 'fast' }).catch((e) => e.message)]`, 2, deep...))
	got := <-ended
	if got.Outcome != contract.TurnCompleted || !strings.HasPrefix(got.Final, `["x on deep-model","profile \"fast\" is not in`) {
		t.Fatalf("turn %+v", got)
	}
}

func TestAFailedAgentThrowsAndBecomesAnEntryInParallel(t *testing.T) {
	h := newCrewHarness(t)
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		if strings.Contains(prompt, "bad") {
			return contract.TurnEnded{Outcome: contract.TurnFailed, Error: "the model refused"}, nil
		}
		return contract.TurnEnded{Final: "fine"}, nil
	}
	got := h.result(crewMeta + `
		const entries = await parallel([() => agent('good'), () => agent('bad one', { label: 'audit-b' })])
		const piped = await pipeline(['x'], () => agent('bad two'), () => agent('never'))
		let thrown = 'nothing'
		try { await agent('bad three') } catch (error) { thrown = error.reason }
		return { entries, piped, thrown }`)
	result := got.(map[string]any)
	entry := result["entries"].([]any)[1].(map[string]any)
	if entry["failed"] != true || entry["step"] != "audit-b" || entry["reason"] != "the model refused" {
		t.Fatalf("entry %v", entry)
	}
	if piped := result["piped"].([]any)[0].(map[string]any); piped["failed"] != true || piped["step"] != "agent: bad two" {
		t.Fatalf("pipeline entry %v", piped)
	}
	if result["thrown"] != "the model refused" {
		t.Fatalf("thrown %v", result["thrown"])
	}
	if h.started.Load() != 4 {
		t.Fatalf("%d workers: a failed stage must skip the next", h.started.Load())
	}
	failed := 0
	for _, step := range h.journal.steps {
		if step.State == contract.WorkflowStepFailed {
			failed++
		}
	}
	if failed != 3 {
		t.Fatalf("%d failed steps in the journal", failed)
	}
}

func TestAThrownErrorFailsTheRun(t *testing.T) {
	h := newCrewHarness(t)
	ended := h.run(crewMeta + `await agent('fine'); throw new Error('the sweep found nothing')`)
	if ended.Outcome != contract.TurnFailed || ended.Cause != contract.CauseWorkflowFailed || !strings.Contains(ended.Error, "the sweep found nothing") {
		t.Fatalf("turn %+v", ended)
	}
	if ended = h.run(crewMeta + `this is not javascript`); ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "SyntaxError") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestClockAndRandomnessThrow(t *testing.T) {
	h := newCrewHarness(t)
	got := h.result(crewMeta + `
		const blocked = []
		for (const thunk of [() => Date.now(), () => Math.random(), () => new Date(), () => Date()]) {
			try { thunk(); blocked.push("allowed") } catch (error) { blocked.push(error.message) }
		}
		return { blocked, fixed: new Date(0).toISOString() }`)
	result := got.(map[string]any)
	if fmt.Sprint(result["blocked"]) != "[Date.now() would differ when the run resumes. Pass the value in args. Math.random() would differ when the run resumes. Pass the value in args. new Date() would differ when the run resumes. Pass the value in args. class constructors must be invoked with 'new']" || result["fixed"] != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("result %v", result)
	}
}

func TestAProfileOutsideTheListFails(t *testing.T) {
	h := newCrewHarness(t)
	got := h.result(crewMeta + `return await parallel([() => agent('x', { profile: 'unlisted' })])`)
	entry := got.([]any)[0].(map[string]any)
	if entry["failed"] != true || !strings.Contains(entry["reason"].(string), "meta.profiles") || h.started.Load() != 0 {
		t.Fatalf("entry %v, %d workers", entry, h.started.Load())
	}
}

func TestNestingIsOneLevel(t *testing.T) {
	h := newCrewHarness(t)
	child := "export const meta = { name: 'child', profiles: ['fast'] }\nreturn { got: await agent('child ' + args.n), nested: await workflow('child.js').catch((e) => e.message) }"
	if err := os.WriteFile(filepath.Join(h.workdir, "child.js"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	script := crewMeta + `return [await workflow('child.js', { n: 1 }), await workflow('child.js', { n: 1 })]`
	got := h.result(script).([]any)
	first := got[0].(map[string]any)
	if first["got"] != "done: child 1" || !strings.Contains(first["nested"].(string), "one level") {
		t.Fatalf("child %v", first)
	}
	if h.started.Load() != 2 {
		t.Fatalf("%d workers: two calls of one child are duplicates, each with its own steps", h.started.Load())
	}
	// A rerun reads the child again. An edit reruns only the changed step, because its siblings keep their parent's key.
	if err := os.WriteFile(filepath.Join(h.workdir, "child.js"), []byte(child+"\n// edited"), 0600); err != nil {
		t.Fatal(err)
	}
	h.result(script)
	if h.started.Load() != 2 {
		t.Fatalf("%d workers after a comment", h.started.Load())
	}
}

func TestRunIsOneAtATimeAndKeepsItsResult(t *testing.T) {
	h := newCrewHarness(t)
	script := crewMeta + `
		const lock = ` + "`mkdir lock || exit 9; sleep 0.2; rmdir lock; echo $WHO`" + `
		const outs = await parallel(['a', 'b', 'c'].map((who) => () => run(lock, { env: { WHO: who } })))
		const soft = await run(['sh', '-c', 'exit 3'], { check: false })
		const hard = await run('exit 4').catch((error) => error.reason)
		return { outs: outs.map((out) => out.stdout.trim()), soft: soft.code, hard }`
	got := h.result(script).(map[string]any)
	if fmt.Sprint(got["outs"]) != "[a b c]" || got["soft"] != float64(3) || !strings.HasPrefix(got["hard"].(string), "exit code 4") {
		t.Fatalf("result %v", got)
	}
	if err := os.WriteFile(filepath.Join(h.workdir, "lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Were they to run again, each would now exit 9.
	if again := h.result(script).(map[string]any); fmt.Sprint(again["outs"]) != "[a b c]" {
		t.Fatalf("stored commands ran again: %v", again)
	}
}

func TestPostIsAStepSoAResumeDoesNotRepeatIt(t *testing.T) {
	h := newCrewHarness(t)
	script := crewMeta + `await post('@root', 'halfway', { files: 3 }); log('posted'); return 'done'`
	h.result(script)
	h.result(script)
	if len(h.journal.posts) != 1 {
		t.Fatalf("%d posts", len(h.journal.posts))
	}
	post := h.journal.posts[0]
	if !slices.Equal(post.Mentions, []string{"@root"}) || !strings.Contains(post.Text, `"files":3`) || len(post.ClientID) != 64 {
		t.Fatalf("post %+v", post)
	}
	found := false
	for key, name := range h.journal.names {
		found = found || name == "log: posted" && h.journal.steps[key].State == contract.WorkflowStepDone
	}
	if !found {
		t.Fatalf("no log step in %v", h.journal.names)
	}
}

// status() is no step, so a resume writes its lines again, and a lane tags a step without changing its key.
func TestStatusIsNotAStepAndALaneKeepsTheKey(t *testing.T) {
	h := newCrewHarness(t)
	tagged := crewMeta + `status('writing'); const a = await agent('write g1', { label: 'write', lane: 'g1' }); await run('true', { lane: 'g1' })
await post('@root', 'g1 written', null, { lane: 'g1' }); status('g1 written'); return a`
	if got := h.result(tagged); got != "done: write g1" {
		t.Fatalf("result %v", got)
	}
	if !slices.Equal(h.journal.lines, []string{"writing", "g1 written"}) {
		t.Fatalf("lines %q", h.journal.lines)
	}
	lanes := 0
	for key, name := range h.journal.names {
		if strings.Contains(name, "writing") {
			t.Fatalf("a status line became a step: %q", name)
		}
		if h.journal.lanes[key] == "g1" {
			lanes++
		}
	}
	if len(h.journal.names) != 3 || lanes != 3 {
		t.Fatalf("steps %v, lanes %v", h.journal.names, h.journal.lanes)
	}
	// The same script without its lanes finds every stored step, and posts nothing twice.
	plain := crewMeta + `status('writing'); const a = await agent('write g1', { label: 'write' }); await run('true')
await post('@root', 'g1 written', null); status('g1 written'); return a`
	if h.result(plain); h.started.Load() != 1 || len(h.journal.posts) != 1 || len(h.journal.names) != 3 {
		t.Fatalf("%d workers, %d posts, steps %v", h.started.Load(), len(h.journal.posts), h.journal.names)
	}
	if !slices.Equal(h.journal.lines, []string{"writing", "g1 written", "writing", "g1 written"}) {
		t.Fatalf("lines after the resume %q", h.journal.lines)
	}
	if ended := h.run(crewMeta + `return await post('@root', 'x', null, { label: 'x' })`); ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "post takes only {lane}") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestASchemaGetsThreeAttempts(t *testing.T) {
	h := newCrewHarness(t)
	var prompts []string
	var mu sync.Mutex
	replies := []string{"not json", `{"count": "many"}`, "```json\n{\"count\": 3}\n```"}
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return contract.TurnEnded{Final: replies[len(prompts)-1]}, nil
	}
	schema := `{ schema: { type: 'object', required: ['count'], properties: { count: { type: 'number' } } } }`
	got := h.result(crewMeta + `return await agent('count them', ` + schema + `)`).(map[string]any)
	if got["count"] != float64(3) || len(prompts) != 3 || !strings.Contains(prompts[0], "JSON Schema") || !strings.Contains(prompts[1], "does not match") {
		t.Fatalf("%v from %q", got, prompts)
	}
	h = newCrewHarness(t)
	h.reply = func(string, string) (contract.TurnEnded, error) { return contract.TurnEnded{Final: "never json"}, nil }
	ended := h.run(crewMeta + `return await agent('count them', ` + schema + `)`)
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "3 attempts") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestAProfileLimitBoundsItsWorkers(t *testing.T) {
	h := newCrewHarness(t)
	var running, peak atomic.Int32
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		now := running.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return contract.TurnEnded{Final: prompt}, nil
	}
	h.run(crewMeta+`return await parallel(['a', 'b', 'c', 'd', 'e', 'f'].map((n) => () => agent(n)))`, contract.WorkflowProfile{Name: "fast", MaxConcurrent: 2})
	if peak.Load() != 2 {
		t.Fatalf("peak %d workers with a limit of 2", peak.Load())
	}
}

func TestStoppingARunStoresNothing(t *testing.T) {
	h := newCrewHarness(t)
	entered := make(chan struct{})
	h.reply = func(string, string) (contract.TurnEnded, error) {
		close(entered)
		time.Sleep(200 * time.Millisecond)
		return contract.TurnEnded{}, errors.New("killed")
	}
	state, sessions := t.TempDir(), t.TempDir()
	crew, err := NewCrew(CrewConfig{Workflow: contract.BoxWorkflow{Script: crewMeta + `return await agent('long')`, Profiles: []contract.WorkflowProfile{{Name: "fast"}}, Workers: 1}, Engine: h.journal, Transcript: NewTranscript(&transcriptEngine{}, sessions), Workdir: h.workdir, Sessions: sessions, State: state,
		Open: func(context.Context, contract.WorkflowProfile, string, string) (crewWorker, error) {
			return &fakeWorker{reply: h.reply, file: filepath.Join(sessions, "w.jsonl")}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-entered
		_ = crew.Abort(context.Background())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ended, err := crew.Start(ctx, "turn_1", []contract.TurnInput{{Delivery: 7}}, func() {})
	if err != nil || ended.Outcome != contract.TurnStopped {
		t.Fatalf("%+v, %v", ended, err)
	}
	if len(h.journal.steps) != 0 {
		t.Fatalf("a stopped worker stored %v", h.journal.steps)
	}
}

// A reconnect resends the turn. The script is already running, so nothing starts again: an engine restart costs a crew nothing.
func TestAReconnectDoesNotRestartTheScript(t *testing.T) {
	h := newCrewHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.reply = func(string, string) (contract.TurnEnded, error) {
		once.Do(func() { close(entered) })
		<-release
		return contract.TurnEnded{Final: "done"}, nil
	}
	state, sessions := t.TempDir(), t.TempDir()
	crew, err := NewCrew(CrewConfig{Workflow: contract.BoxWorkflow{Script: crewMeta + `return await agent('long')`, Profiles: []contract.WorkflowProfile{{Name: "fast"}}, Workers: 1}, Engine: h.journal, Transcript: NewTranscript(&transcriptEngine{}, sessions), Workdir: h.workdir, Sessions: sessions, State: state,
		Open: func(context.Context, contract.WorkflowProfile, string, string) (crewWorker, error) {
			h.started.Add(1)
			return &fakeWorker{reply: h.reply, file: filepath.Join(sessions, "w.jsonl")}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingTurnOutput{ended: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: crew, Sink: sink}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_1", Inputs: []contract.TurnInput{{Delivery: 7, Text: "run"}}, Model: contract.ModelChoice{ID: "m"}}}
	if err := runner.Handle(ctx, start); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := runner.Handle(ctx, start); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case ended := <-sink.ended:
		if ended.Outcome != contract.TurnCompleted || ended.Final != "done" || h.started.Load() != 1 {
			t.Fatalf("%+v after %d workers", ended, h.started.Load())
		}
	case <-ctx.Done():
		t.Fatal("the turn did not end")
	}
}

type recordingTurnOutput struct{ ended chan contract.TurnEnded }

func (recordingTurnOutput) DeliveryDone(context.Context, contract.DeliverySeq) error { return nil }
func (recordingTurnOutput) Compacted(context.Context, contract.BoxCompactedRequest) error {
	return nil
}
func (r *recordingTurnOutput) TurnOutput(_ context.Context, result contract.TurnEnded) error {
	r.ended <- result
	return nil
}

func TestEveryFailureCountsInTheJournal(t *testing.T) {
	h := newCrewHarness(t)
	got := h.result(crewMeta + `
		return await parallel([
			() => agent('x', { timeut: 60 }),
			() => run('true', { nope: 1 }),
			() => run(7),
			() => post(7, 'text'),
			() => workflow('missing.js'),
			() => { throw new Error('a plain bug') },
		])`).([]any)
	if len(got) != 6 {
		t.Fatalf("result %v", got)
	}
	failed := 0
	for _, step := range h.journal.steps {
		if step.State == contract.WorkflowStepFailed {
			failed++
		}
	}
	if failed != 6 || h.started.Load() != 0 {
		t.Fatalf("%d failed steps of 6 in %v, %d workers", failed, h.journal.steps, h.started.Load())
	}
}

func TestAResultJSONCannotHoldFailsTheRun(t *testing.T) {
	h := newCrewHarness(t)
	ended := h.run(crewMeta + `return () => 1`)
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "JSON cannot hold") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestAChildThatThrowsNothingStillFails(t *testing.T) {
	h := newCrewHarness(t)
	if err := os.WriteFile(filepath.Join(h.workdir, "child.js"), []byte("throw new Error()"), 0600); err != nil {
		t.Fatal(err)
	}
	h.result(crewMeta + `return await parallel([() => workflow('child.js')])`)
	failed := 0
	for _, step := range h.journal.steps {
		if step.State == contract.WorkflowStepFailed && step.Reason != "" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("journal %+v", h.journal.steps)
	}
}

// A worker names its file as it starts, so the run page can open it while it runs.
func TestAWorkerNamesItsFileBeforeItEnds(t *testing.T) {
	h := newCrewHarness(t)
	var reported []string
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		h.journal.mu.Lock()
		defer h.journal.mu.Unlock()
		for _, file := range h.journal.files {
			reported = append(reported, file)
		}
		return contract.TurnEnded{Final: prompt}, nil
	}
	h.run(crewMeta + `return await agent('one')`)
	if len(reported) != 1 || reported[0] != "w1.jsonl" {
		t.Fatalf("while it ran, the journal knew %v", reported)
	}
	for key, end := range h.journal.steps {
		if end.Transcript != "w1.jsonl" {
			t.Fatalf("step %s ended naming %q", key, end.Transcript)
		}
	}
}

// A new box takes over a turn whose input an earlier box took, so its workers learn the delivery from the turn itself.
func TestARecoveredCrewFilesItsWorkersUnderItsTurn(t *testing.T) {
	h := newCrewHarness(t)
	sessions := t.TempDir()
	var delivery contract.DeliverySeq
	crew, err := NewCrew(CrewConfig{Workflow: contract.BoxWorkflow{Script: crewMeta + `return await agent('one')`, Profiles: []contract.WorkflowProfile{{Name: "fast"}}, Workers: 1}, Engine: h.journal, Transcript: NewTranscript(&transcriptEngine{}, sessions), Workdir: h.workdir, Sessions: sessions, State: t.TempDir(),
		Open: func(context.Context, contract.WorkflowProfile, string, string) (crewWorker, error) {
			return recordingWorker{fakeWorker: &fakeWorker{reply: h.reply, file: filepath.Join(sessions, "w.jsonl")}, delivery: &delivery}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if _, settled, err := crew.Recover("turn_1", []contract.TurnInput{{Delivery: 9}}); settled || err != nil {
		t.Fatalf("settled %v, %v", settled, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := crew.Continue(ctx, "turn_1", func() {}); err != nil {
		t.Fatal(err)
	}
	if delivery != 9 {
		t.Fatalf("the worker carried delivery %d", delivery)
	}
}

type recordingWorker struct {
	*fakeWorker
	delivery *contract.DeliverySeq
}

func (w recordingWorker) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	*w.delivery = inputs[0].Delivery
	return w.fakeWorker.Start(ctx, id, inputs, started)
}

func TestAForgottenStepFinishesBeforeTheRunEnds(t *testing.T) {
	h := newCrewHarness(t)
	gate := make(chan struct{})
	h.hold = func(ctx context.Context, _ string) error {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, ended := h.start(crewMeta + `agent("slow"); return "returned"`)
	waitFor(t, "the forgotten step to start", func() bool { return h.started.Load() == 1 })
	select {
	case turn := <-ended:
		t.Fatalf("the run ended with its step running: %+v", turn)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	turn := <-ended
	if turn.Outcome != contract.TurnCompleted || turn.Final != "returned" {
		t.Fatalf("turn %+v", turn)
	}
	if done := h.journal.steps; len(done) != 1 {
		t.Fatalf("%d steps in the journal", len(done))
	}
}
