// Package workflow runs a workflow script: a plain-JavaScript program using
// agent()/workflow()/parallel()/pipeline()/phase()/log() to orchestrate worker profiles.
// Scripts run on an embedded JS engine (goja); agent() calls fan out to real
// agent CLIs through the harness package, capped by a concurrency semaphore.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/console"
	"github.com/dop251/goja_nodejs/eventloop"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/textutil"
	"dyna-agent/internal/workflowresolve"
	"dyna-agent/internal/worktrees"
)

const (
	minimumAgentTimeout       = 30 * time.Minute
	defaultAgentTimeout       = 5 * time.Hour
	defaultJournalIdle        = 20 * time.Minute
	worktreeJournalEntryLimit = 20
)

// Only flag a complete, unambiguous wait-only prompt; longer prompts may ask for judgment.
var waitOnlyAgentPrompt = regexp.MustCompile(`(?i)^\s*(?:wait|poll) for file [^\s]+\s*$`)

// processEnvAllowlist is deliberately small. Workflow scripts are not Node
// programs, so process.env must not become an alternate path to host secrets.
var processEnvAllowlist = []string{
	"HOME",
	"USER",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"LANG",
}

// Options configures one workflow execution.
type Options struct {
	ScriptSrc         string
	ScriptPath        string // invoking script path; relative workflow refs use its directory
	Args              any
	Store             *profile.Store
	Run               *runstore.Run        // persistence sink
	OnEvent           func(runstore.Event) // optional live sink (CLI progress)
	OnWarning         func(string)         // optional parse-time warning sink
	WorkDir           string               // cwd for workers
	WorktreeRetention string               // changed|all|none; empty resolves worktrees.retention from settings
	MaxConc           int                  // 0 => min(16, cores-2)
	MaxAgents         int                  // lifetime agent() cap; 0 => 1000
	Cache             *Cache               // resume cache (nil = fresh run)
	WorkflowDir       string               // optional name-keyed workflow registry directory
	// Poster delivers post(to, text) from a workflow script as run:<runID>.
	// It is supplied by the CLI so the engine stays independent of Dyna
	// routing. A non-nil error with a non-empty result ID means the post was
	// stored but at least one delivery failed.
	Poster func(ctx context.Context, runID, stepKey string, mentions []string, text string) (PostResult, error)
	// Engine starts agent() sessions and submits and waits for job() calls. A
	// run coordinator passes its engine connection; tests pass a fake.
	Engine EngineCaller
	// Commands runs the commands of run() calls as managed shells. Nil makes
	// run() fail; the run coordinator passes the shells-backed runner.
	Commands CommandRunner
	// MaxCommands caps the run() commands that execute at once, across the
	// run's parallel() calls; 0 => 4.
	MaxCommands int
	// Paused, when it returns true, blocks new worker launches (running
	// workers finish). Polled while waiting to start each agent.
	Paused func() bool
	// ResumeFrom is the prior run id when this execution is a --resume.
	// Recorded on plan.json as resumedFrom; it does not change cache keys.
	ResumeFrom string
	// RecordManifest records the calls accepted by this run for later safe replay.
	RecordManifest bool
	// Replay gates side effects until every recorded call is matched.
	Replay *ReplayBarrier
	// ReplaySources contains nested source hashes captured before execution.
	ReplaySources      map[string]string
	CheckReplaySources bool
	// JobNamespace is the stable identity shared by every run in a resume
	// chain. The workflow service sets it for durable job idempotency keys.
	JobNamespace string
}

type outcome struct {
	resultJSON string
	err        error
}

// Execute runs the script to completion and returns the workflow's return
// value as JSON.
func Execute(ctx context.Context, o Options) (string, error) {
	return execute(ctx, o, nil, 0, "", nil)
}

// sharedState owns every limit whose meaning is "per run", including calls
// made by nested workflows. Each script still gets an isolated JS runtime.
type stepActivity struct {
	publisher *activity.Publisher
	record    activity.Record
}

var ErrManifestWrite = errors.New("workflow manifest write failed")

type sharedState struct {
	activityMu sync.Mutex
	activities map[activity.Key]*stepActivity
	sem        chan struct{}
	// cmdSem limits the run() commands that execute at once.
	cmdSem chan struct{}

	mu                sync.Mutex
	scratchID         string
	scratchWarning    sync.Once
	manifestFailed    bool
	agentSeq          int
	execSeq           int // run() and job() steps; counted apart from agents and their cap
	jobNamespace      string
	jobOccurrences    map[string]int
	postOccurrences   map[string]int
	workflowSeq       int
	profSems          map[string]chan struct{}
	profCalls         map[string]int
	capErr            error // first profile call-cap failure anywhere in this run's tree
	abortRoot         func(error)
	worktreeRetention string
	worktrees         []worktrees.Record
	agentsStarted     bool // some agent() call asked the engine for a session
}

type preparedScript struct {
	source, workflowPlanID string
}

func execute(ctx context.Context, o Options, shared *sharedState, depth int, workflowID string, prepared *preparedScript) (result string, resultErr error) {
	if depth == 0 && o.Replay != nil {
		defer func() { resultErr = errors.Join(resultErr, o.Replay.Finish()) }()
	}
	maxConc := o.MaxConc
	if maxConc <= 0 {
		maxConc = runtime.NumCPU() - 2
		if maxConc > 16 {
			maxConc = 16
		}
		if maxConc < 2 {
			maxConc = 2
		}
	}
	var worktreeWarning string
	if shared == nil {
		jobNamespace := o.JobNamespace
		if jobNamespace == "" {
			jobNamespace = parentRunID(o.Run)
		}
		if jobNamespace == "" {
			jobNamespace = runstore.NewID()
		}
		worktreeRetention, warning, err := resolveWorktreeRetention(o.WorktreeRetention)
		if err != nil {
			return "", err
		}
		worktreeWarning = warning
		shared = &sharedState{
			scratchID: runstore.NewID(), jobNamespace: jobNamespace, jobOccurrences: map[string]int{}, postOccurrences: map[string]int{},
			sem:      make(chan struct{}, maxConc),
			cmdSem:   newCommandLimit(o.MaxCommands),
			profSems: map[string]chan struct{}{}, profCalls: map[string]int{}, worktreeRetention: worktreeRetention,
		}
	}
	if depth == 0 && o.RecordManifest {
		defer func() {
			shared.mu.Lock()
			failed := shared.manifestFailed
			shared.mu.Unlock()
			if failed {
				resultErr = errors.Join(resultErr, ErrManifestWrite)
			}
		}()
	}
	if warning := resumeNondeterminismWarning(o.ScriptSrc); warning != "" {
		if o.RecordManifest {
			if err := runstore.MarkWorkflowManifestUnsafe(ctx, o.Run.Meta.ID, warning); err != nil {
				shared.mu.Lock()
				shared.manifestFailed = true
				shared.mu.Unlock()
				return "", fmt.Errorf("mark workflow manifest unsafe: %w", err)
			}
		}
		if o.OnWarning != nil {
			o.OnWarning(warning)
		}
	}
	if prepared == nil {
		source, id, err := prepareScript(o, workflowID)
		if err != nil {
			return "", err
		}
		prepared = &preparedScript{source: source, workflowPlanID: id}
	}
	wrapped, err := transform(prepared.source)
	if err != nil {
		return "", err
	}
	eng := &engine{opts: o, shared: shared, depth: depth, workflowID: workflowID, workflowPlanID: prepared.workflowPlanID}
	if depth == 0 {
		defer shared.closeActivities()
	}

	loop := eventloop.NewEventLoop(eventloop.EnableConsole(false))
	eng.done = make(chan outcome, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	eng.ctx = ctx
	eng.cancel = cancel
	eng.loop = loop
	if worktreeWarning != "" {
		eng.emitLog(runstore.LogProvenanceSystem, worktreeWarning)
	}
	if depth == 0 {
		shared.abortRoot = func(err error) {
			eng.settle(outcome{err: err})
			eng.cancel()
		}
	}

	loop.Start()
	defer loop.Stop()

	loop.RunOnLoop(func(vm *goja.Runtime) {
		console.Enable(vm)
		vm.SetAsyncContextTracker(planAsyncContextTracker{vm: vm})
		// goja cannot see the context. A script that never yields, such as
		// while (true) {}, would hold the loop forever; a cancel or deadline
		// interrupts it. loop.Stop runs before cancel, so a finished run is never interrupted.
		context.AfterFunc(ctx, func() { vm.Interrupt(ctx.Err()) })
		if err := eng.bind(vm); err != nil {
			eng.settle(outcome{err: err})
			return
		}
		if _, err := vm.RunScript("workflow.js", wrapped); err != nil {
			eng.settle(outcome{err: fmt.Errorf("script error: %w", err)})
		}
	})

	select {
	case res := <-eng.done:
		eng.stopWorkers()
		return res.resultJSON, res.err
	case <-ctx.Done():
		// Prefer a terminal workflow result if it raced with cancellation.
		select {
		case res := <-eng.done:
			eng.stopWorkers()
			return res.resultJSON, res.err
		default:
		}
		eng.stopWorkers()
		return "", ctx.Err()
	}
}

func resolveWorktreeRetention(value string) (string, string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		loaded, err := settings.Load(settings.DefaultPath())
		if err != nil {
			return settings.WorktreesRetentionChanged, fmt.Sprintf("could not load worktree retention setting; using %q: %v", settings.WorktreesRetentionChanged, err), nil
		}
		value = loaded.WorktreesRetention
	}
	if !settings.ValidWorktreesRetention(value) {
		return "", "", fmt.Errorf("worktree retention must be one of changed, all, none")
	}
	return value, "", nil
}

type engine struct {
	opts           Options
	shared         *sharedState
	depth          int
	workflowID     string
	workflowPlanID string
	ctx            context.Context
	cancel         context.CancelFunc
	loop           *eventloop.EventLoop
	vm             *goja.Runtime // valid only on the loop thread
	done           chan outcome
	settleOnce     sync.Once
	phaseMu        sync.Mutex
	curPhase       string
	curLane        string // lane and stage of the current phase, when its script named them
	curStage       string
	workerMu       sync.Mutex
	workers        sync.WaitGroup
	stopping       bool
	planMu         sync.Mutex
	planGroupID    string
	planStage      int
	planItem       int
	planIndexSet   bool
	// asyncPending is accessed only on this engine's event-loop thread.
	asyncPending  int
	overlapMarked bool
}

type callPlan struct {
	nodeID, groupID, wfPlanID string
	stage, item               *int
}

func (e *engine) manifestWriteFailed() {
	e.shared.mu.Lock()
	e.shared.manifestFailed = true
	e.shared.mu.Unlock()
}

// settle delivers the run's single outcome (first caller wins).
func (e *engine) settle(o outcome) {
	e.settleOnce.Do(func() { e.done <- o })
}

// stopForUpdate settles the whole run as stopped for a dyna update: no new
// step starts, running steps are canceled, and the coordinator reports the
// outcome so the engine can report it once (D-402).
// A nil abortRoot cannot happen for a step: it is set before the script runs.
func (e *engine) stopForUpdate(cause error) {
	if e.shared.abortRoot == nil {
		return
	}
	e.shared.abortRoot(fmt.Errorf("%w: %v", ErrStoppedForUpdate, cause))
}

// finish delivers the script outcome unless a profile call cap was exceeded
// anywhere in this run's tree (root or a nested workflow()). A cap failure is
// surfaced only after every accepted worker owned by this engine has written
// its terminal events and journal entry; those workers must remain
// resume-eligible. capErr lives in sharedState because per-profile caps apply
// across nested workflow() calls, not just the engine that happened to trip it.
func (e *engine) finish(o outcome) {
	e.shared.mu.Lock()
	capErr := e.shared.capErr
	e.shared.mu.Unlock()
	if capErr == nil {
		e.settle(o)
		return
	}

	e.workerMu.Lock()
	e.stopping = true
	e.workerMu.Unlock()
	go func() {
		e.workers.Wait()
		e.settle(outcome{err: capErr})
	}()
}

func (e *engine) beginWorker() bool {
	e.workerMu.Lock()
	defer e.workerMu.Unlock()
	if e.stopping || e.ctx.Err() != nil {
		return false
	}
	e.workers.Add(1)
	return true
}

// stopWorkers prevents new launches, cancels every in-flight harness, and
// waits for their terminal journal/event writes before the caller closes Run.
func (e *engine) stopWorkers() {
	e.workerMu.Lock()
	e.stopping = true
	e.cancel()
	e.workerMu.Unlock()
	e.workers.Wait()
	e.closeAgents()
}

// closeAgent has the engine close and stop one agent's session.
func (e *engine) closeAgent(id int) {
	if e.opts.Engine == nil {
		return
	}
	ref := callspec.RunAgentRef{Run: e.runID(), Agent: id}
	// The caller keeps its slots and worktree until the stop is confirmed, so a
	// call cut off by an engine restart is asked again with no time limit
	// (closing twice is safe). Only a refusal ends it, and then the engine's
	// own cleanup of an ended run's sessions is what remains.
	ctx := context.WithoutCancel(e.ctx)
	err := retryEngine(ctx, 0, func() error { return e.opts.Engine.Call(ctx, "CloseRunAgent", []any{ref}, nil) })
	if err != nil {
		e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("could not close agent %d's session: %v", id, err))
	}
}

// closeAgents has the engine close the run's agent sessions that are still
// open: the run joined, failed or was canceled, and no agent outlives it. Only
// the run's root script does it, and only after it started an agent.
func (e *engine) closeAgents() {
	if e.depth != 0 || e.opts.Engine == nil {
		return
	}
	e.shared.mu.Lock()
	started := e.shared.agentsStarted
	e.shared.mu.Unlock()
	if !started {
		return
	}
	if err := e.opts.Engine.Call(context.WithoutCancel(e.ctx), "CloseRunAgents", []any{e.runID()}, nil); err != nil {
		e.emitLog(runstore.LogProvenanceSystem, "could not close the run's agent sessions: "+err.Error())
	}
}

func (e *engine) emit(ev runstore.Event) {
	e.observeActivity(ev)
	if ev.Workflow == "" {
		ev.Workflow = e.workflowID
	}
	if ev.WorkflowPlanID == "" {
		if e.workflowPlanID != "" {
			ev.WorkflowPlanID = e.workflowPlanID
		} else if e.workflowID != "" {
			ev.WorkflowPlanID = e.workflowID
		} else {
			ev.WorkflowPlanID = plan.RootWorkflowID
		}
	}
	if e.opts.Run != nil {
		e.opts.Run.Append(ev)
	}
	if e.opts.OnEvent != nil {
		ev.TS = time.Now().UnixMilli()
		e.opts.OnEvent(ev)
	}
}

func (p callPlan) apply(ev *runstore.Event) {
	if p.nodeID != "" {
		ev.PlanNodeID = p.nodeID
	}
	if p.groupID != "" {
		ev.PlanGroupID = p.groupID
	}
	if ev.WorkflowPlanID == "" {
		ev.WorkflowPlanID = p.wfPlanID
	}
	ev.StageIndex = p.stage
	ev.ItemIndex = p.item
}

func (e *engine) emitLog(provenance, msg string) {
	e.emit(runstore.Event{
		T:          "log",
		Msg:        textutil.ClipOneLine(msg, 4000),
		Provenance: runstore.CanonicalLogProvenance(provenance),
	})
}

// bind installs the Go-backed globals: __spawn, __phase, __log, __logSystem,
// __finish, __fail, args, process, profiles, then evaluates the JS prelude that
// builds the public API
// (agent/workflow/msg/parallel/pipeline/phase/log)
// on top of them.
// spawn implements one agent() call. Called on the loop thread; returns a
// Promise resolved from a worker goroutine.
func (e *engine) spawn(call goja.FunctionCall) goja.Value {
	vm := e.vm // spawn always runs on the loop thread
	promise, resolveFn, rejectFn := vm.NewPromise()
	var release func()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) {
			if release != nil {
				release()
				release = nil
			}
			resolveFn(vm.ToValue(v))
		})
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) {
			if release != nil {
				release()
				release = nil
			}
			rejectFn(vm.NewGoError(err))
		})
	}

	prompt := call.Argument(0).String()
	if waitOnlyAgentPrompt.MatchString(prompt) {
		e.emitLog(runstore.LogProvenanceSystem, "agent() appears to only wait for a file; use run() for deterministic waiting and set a timeout")
	}
	opts := call.Argument(1)

	profName, label, phase, lane, stage, cwd, planNodeID := "", "", "", "", "", e.opts.WorkDir, ""
	timeout := defaultAgentTimeout
	timeoutSet := false
	var schemaJSON, isolation string
	worktreeRetention := e.shared.worktreeRetention
	var optionErr error
	groupFromOpts := ""
	var stageFromOpts, itemFromOpts int
	hasStage, hasItem := false, false
	if o, ok := opts.Export().(map[string]any); ok && o != nil {
		if v, ok := o["profile"].(string); ok {
			profName = v
		}
		if v, ok := o["label"].(string); ok {
			label = v
		}
		if v, ok := o["phase"].(string); ok {
			phase = v
		}
		if v, ok := o["lane"].(string); ok {
			lane = v
		}
		if v, ok := o["stage"].(string); ok {
			stage = v
		}
		if v, ok := o["cwd"].(string); ok && v != "" {
			cwd = v
		}
		if raw, exists := o["isolation"]; exists && raw != nil {
			isolationOpts, ok := raw.(map[string]any)
			if !ok {
				optionErr = fmt.Errorf("agent isolation must be an object like { type: \"worktree\", retention?: \"changed\" | \"all\" | \"none\" }; the string form is not supported")
			} else {
				typeName, ok := isolationOpts["type"].(string)
				if !ok || strings.TrimSpace(typeName) == "" {
					optionErr = fmt.Errorf("agent isolation.type must be \"worktree\"")
				} else {
					isolation = strings.ToLower(strings.TrimSpace(typeName))
					if isolation != "worktree" {
						optionErr = fmt.Errorf("unknown isolation type %q (only \"worktree\" is supported)", isolation)
					}
				}
				if rawRetention, exists := isolationOpts["retention"]; optionErr == nil && exists {
					retention, ok := rawRetention.(string)
					if ok {
						retention = strings.ToLower(strings.TrimSpace(retention))
					}
					if !ok || !settings.ValidWorktreesRetention(retention) {
						optionErr = fmt.Errorf("agent isolation.retention must be one of changed, all, none")
					} else {
						worktreeRetention = retention
					}
				}
			}
		}
		if v, ok := o["timeout"]; ok {
			if f, ok := toFloat(v); ok && f > 0 {
				timeout = time.Duration(f * float64(time.Second))
				timeoutSet = true
			}
		}
		if v, ok := o["schema"]; ok && v != nil {
			// A schema that is not JSON fails the call; it must never run unvalidated.
			if b, err := json.Marshal(v); err != nil {
				optionErr = errors.Join(optionErr, fmt.Errorf("agent schema is not valid JSON: %w", err))
			} else {
				schemaJSON = string(b)
			}
		}
		if v, ok := o["__planNodeId"].(string); ok {
			planNodeID = v
		}
		if v, ok := o["__planGroupId"].(string); ok && v != "" {
			groupFromOpts = v
		}
		if v, ok := optInt(o, "__stageIndex"); ok {
			stageFromOpts, hasStage = v, true
		}
		if v, ok := optInt(o, "__itemIndex"); ok {
			itemFromOpts, hasItem = v, true
		}
	}

	p, profileErr := e.opts.Store.WorkflowProfile(profName)
	if profileErr != nil {
		reject(profileErr)
		return vm.ToValue(promise)
	}
	prof := *p
	timeout = resolveAgentTimeout(timeout, timeoutSet, prof)
	// Workflow workers always run as leaves. The harness calls below pass
	// ExecutionOptions{DisableNativeSubagents: true} for every workflow
	// worker, regardless of profile. That is an execution concern, not a
	// persisted profile setting, so the profile itself is left unchanged.

	if optionErr != nil {
		reject(optionErr)
		return vm.ToValue(promise)
	}

	maxAgents := e.opts.MaxAgents
	if maxAgents <= 0 {
		maxAgents = 1000
	}
	e.shared.mu.Lock()
	e.shared.agentSeq++
	id := e.shared.agentSeq
	e.shared.mu.Unlock()
	e.phaseMu.Lock()
	if phase == "" {
		phase = e.curPhase
		if lane == "" && stage == "" {
			lane, stage = e.curLane, e.curStage
		}
	}
	e.phaseMu.Unlock()
	if id > maxAgents {
		reject(fmt.Errorf("agent cap reached (%d); raise with --max-agents", maxAgents))
		return vm.ToValue(promise)
	}
	cacheLabel := label
	if label == "" {
		label = fmt.Sprintf("agent-%d", id)
	}
	// Match exec.Cmd.Dir semantics: relative cwd values are relative to the
	// host process, and an empty cwd inherits its working directory.
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		reject(fmt.Errorf("resolve worker cwd: %w", err))
		return vm.ToValue(promise)
	}
	if err := e.checkReplayCwd(cwd); err != nil {
		reject(err)
		return vm.ToValue(promise)
	}
	key := callKey(prof, prompt, schemaJSON, cwd, cacheLabel, timeout)
	cacheCwd := cwd
	cp := e.snapshotCallPlan(planNodeID)
	if groupFromOpts != "" {
		cp.groupID = groupFromOpts
	}
	if hasStage {
		s := stageFromOpts
		cp.stage = &s
	}
	if hasItem {
		i := itemFromOpts
		cp.item = &i
	}
	emitA := func(ev runstore.Event) {
		cp.apply(&ev)
		ev.Lane, ev.Stage = lane, stage
		e.emit(ev)
	}
	emitA(runstore.Event{T: "agent_start", ID: id, Label: label, Profile: prof.Name, Phase: phase, Preview: textutil.ClipOneLine(prompt, 160)})
	release, err = e.beginReplayAsync()
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	if e.opts.Replay != nil {
		matched, value, replayErr := e.replayCall(runstore.WorkflowCallAgent, key, id)
		if matched || replayErr != nil {
			if matched {
				e.shared.mu.Lock()
				e.shared.profCalls[prof.Name]++
				e.shared.mu.Unlock()
			}
			if replayErr != nil {
				reject(replayErr)
			} else {
				resolve(value)
			}
			return vm.ToValue(promise)
		}
	}
	// Resume: replay a cached result without consuming a worker slot.
	if e.opts.Cache != nil && isolation == "" {
		if cached, ok := e.opts.Cache.pop(key); ok {
			emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase,
				Status: "ok", Cached: true, Preview: cachedSummary(cached)})
			if e.opts.Run != nil {
				e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Cwd: cacheCwd, Prompt: prompt, Result: cached, Cached: true, Workflow: e.workflowID})
			}
			resolve(cached)
			return vm.ToValue(promise)
		}
	}

	// Per-profile limits: users cap how hard a profile may be leaned on
	// (e.g. don't let a workflow spin up 50 concurrent expensive workers).
	// Exceeding a call cap still fails the whole run, but accepted workers drain
	// naturally so their successful journal entries can be reused on resume.
	if prof.MaxCallsPerRun > 0 {
		e.shared.mu.Lock()
		n := e.shared.profCalls[prof.Name]
		if n < prof.MaxCallsPerRun {
			e.shared.profCalls[prof.Name] = n + 1
		}
		overLimit := n >= prof.MaxCallsPerRun
		var capErr error
		if overLimit {
			err := fmt.Errorf("profile %q call limit exceeded (%d per run)", prof.Name, prof.MaxCallsPerRun)
			if e.shared.capErr == nil {
				e.shared.capErr = fmt.Errorf("workflow failed: %v; size the fan-out within the profile's maxCallsPerRun (see `dyna profiles list --json`) or route bulk work to an unlimited profile", err)
			}
			capErr = err
		}
		e.shared.mu.Unlock()
		if overLimit {
			emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: capErr.Error()})
			e.emitLog(runstore.LogProvenanceSystem, "profile call limit exceeded; draining accepted calls before failing run: "+capErr.Error())
			reject(capErr)
			return vm.ToValue(promise)
		}
	}
	var profSem chan struct{}
	if prof.MaxConcurrent > 0 {
		e.shared.mu.Lock()
		profSem = e.shared.profSems[prof.Name]
		if profSem == nil {
			profSem = make(chan struct{}, prof.MaxConcurrent)
			e.shared.profSems[prof.Name] = profSem
		}
		e.shared.mu.Unlock()
	}

	if !e.beginWorker() {
		err := e.ctx.Err()
		if err == nil {
			err = fmt.Errorf("workflow is already finishing")
		}
		emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
		reject(err)
		return vm.ToValue(promise)
	}
	if e.opts.RecordManifest {
		if err := runstore.RegisterWorkflowCall(e.ctx, e.runID(), runstore.WorkflowCallAgent, key, id); err != nil {
			e.manifestWriteFailed()
			e.workers.Done()
			recordErr := fmt.Errorf("record agent manifest intent: %w", err)
			emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: recordErr.Error()})
			reject(recordErr)
			return vm.ToValue(promise)
		}
	}
	journalPath := ""
	failBeforeRun := func(err error) {
		if e.opts.Run != nil && journalPath != "" {
			_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "error", Message: textutil.ClipOneLine(err.Error(), 4000)})
		}
		emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
		reject(err)
	}
	go func() {
		defer e.workers.Done()
		// The journal begins when the engine accepts the call, before any local
		// execution queue. A workflow that finishes while this worker is
		// still queued therefore retains a terminal, resume-safe record.
		journalRoot := ""
		if e.opts.Run != nil {
			var journalErr error
			journalPath, journalErr = e.opts.Run.StartAgentJournal(id, label, prof.Name, phase, prompt)
			if journalErr != nil {
				emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: textutil.ClipOneLine(journalErr.Error(), 2000)})
				reject(fmt.Errorf("start work journal for %s: %w", label, journalErr))
				return
			}
			journalRoot, _ = filepath.Abs(e.opts.Run.Dir)
		}
		if profSem != nil {
			if err := acquireSlot(e.ctx, profSem); err != nil {
				failBeforeRun(err)
				return
			}
			defer func() { <-profSem }()
		}
		if err := acquireSlot(e.ctx, e.shared.sem); err != nil {
			failBeforeRun(err)
			return
		}
		defer func() { <-e.shared.sem }()

		// Pausing blocks new launches but lets running workers finish.
		for e.opts.Paused != nil && e.opts.Paused() {
			select {
			case <-e.ctx.Done():
				failBeforeRun(e.ctx.Err())
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := e.ctx.Err(); err != nil {
			failBeforeRun(err)
			return
		}
		// Stop before a replaced binary starts more work (D-402).
		if executableReplaced() {
			e.stopForUpdate(updateReplacementCause)
			failBeforeRun(updateReplacementCause)
			return
		}
		emitA(runstore.Event{T: "agent_run", ID: id, Label: label, Profile: prof.Name, Phase: phase})
		start := time.Now()

		actx, cancel := context.WithTimeout(e.ctx, timeout)
		defer cancel()

		// Worktree isolation: run the worker on a detached copy of the repo and
		// apply the resolved per-call retention when it finishes.
		retainedDir := ""
		worktreeStatus := ""
		worktreeBase := ""
		worktreeRepo := ""
		var cleanupWT func() bool
		if isolation == "worktree" {
			if e.opts.RecordManifest {
				if err := runstore.MarkWorkflowManifestUnsafe(actx, e.runID(), "agent worktree effects are not in the workflow manifest"); err != nil {
					e.manifestWriteFailed()
					failBeforeRun(fmt.Errorf("mark workflow manifest unsafe: %w", err))
					return
				}
			}
			worktreeRepo, _ = filepath.Abs(cwd)
			if root, rootErr := worktrees.RepositoryRoot(actx, cwd); rootErr == nil {
				worktreeRepo, _ = filepath.Abs(root)
			}
			owner := os.Getenv("DYNA_SESSION")
			if e.opts.Run != nil {
				if e.opts.Run.Meta.OwnerHandoff {
					// Track resolves the current owner in the registry transaction.
					owner = ""
				} else if e.opts.Run.Meta.ParentSession != "" {
					owner = e.opts.Run.Meta.ParentSession
				}
			}
			wt, baseCommit, cleanup, werr := worktrees.CreateForRun(actx, cwd, worktreeRetention, e.runID(), owner)
			if werr != nil {
				if e.opts.Run != nil && journalPath != "" {
					_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "error", Message: textutil.ClipOneLine(werr.Error(), 4000)})
				}
				emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: textutil.ClipOneLine(werr.Error(), 2000)})
				reject(werr)
				return
			}
			cwd = wt
			worktreeBase = baseCommit
			cleanupWT = cleanup
		}

		e.shared.mu.Lock()
		e.shared.agentsStarted = true
		e.shared.mu.Unlock()
		// The agent is a leaf session the engine runs like any other session.
		// The coordinator only asks for it and waits for its first turn.
		agentStart := callspec.RunAgentStart{
			Run: e.runID(), Agent: id, Profile: prof.Name, Dir: cwd, Task: prompt, Schema: schemaJSON,
			Deadline: start.Add(timeout), Journal: journalPath, JournalRoot: journalRoot,
		}
		// A mock profile can exist only in this run (the demo's), so the engine
		// gets its definition. A mock harness holds no credentials.
		if prof.Harness == profile.HarnessMock {
			inline := prof
			agentStart.Inline = &inline
		}
		stepKey := ""
		if e.opts.Run != nil {
			stepKey = key
		}
		state, err := runAgentSession(actx, e.opts.Engine, agentStart, stepKey)
		if err != nil {
			// The call ended without a result (canceled, timed out, or failed):
			// stop the session before this call gives up its slots, so a script
			// that ended with the agent still running leaves nothing running.
			e.closeAgent(id)
			// State a newer dyna wrote fails every call this binary would make;
			// stop once and let the engine report the interruption (D-402).
			if newerSchemaError(err) {
				e.stopForUpdate(err)
			}
		}
		var resultAny any
		rawOut := ""
		if err == nil {
			resultAny, rawOut, err = agentResult(state)
		}
		if e.opts.RecordManifest && state.Done {
			if recordErr := runstore.CompleteWorkflowCall(context.WithoutCancel(actx), e.runID(), runstore.WorkflowCallAgent, key, id, runstore.AgentSessionID(e.runID(), id), resultAny, err); recordErr != nil {
				e.manifestWriteFailed()
				err = fmt.Errorf("record agent manifest result: %w", recordErr)
			}
		}
		dur := time.Since(start)

		if cleanupWT != nil {
			head, status, changed, inspectErr := worktrees.Fingerprint(context.Background(), cwd, worktreeBase)
			forceRetain := inspectErr != nil
			journaled := false
			if worktreeRetention == settings.WorktreesRetentionNone && changed {
				e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s checking safe cleanup under worktrees.retention=none: %s", label, cwd))
				journaled = e.appendWorktreeChanges(id, label, journalPath, cwd, worktreeBase)
				if !journaled {
					forceRetain = true
					e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s retained changed worktree because its changes could not be journaled: %s", label, cwd))
				}
			}
			retained := forceRetain || cleanupWT()
			if retained {
				retainedDir = cwd
				worktreeStatus = "retained"
				e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s retained worktree: %s", label, cwd))
				if !journaled {
					e.appendWorktreeChanges(id, label, journalPath, cwd, worktreeBase)
				}
				if inspectErr != nil {
					e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s could not fingerprint retained worktree: %v", label, inspectErr))
				}
				e.trackRetainedWorktree(worktrees.Record{
					Path: cwd, RunID: e.runID(), AgentID: id, Label: label, Profile: prof.Name,
					Repo: worktreeRepo, Base: worktreeBase, Head: head, Status: status, RetainedAt: time.Now(),
				})
			} else {
				worktreeStatus = "removed"
			}
		}
		if e.opts.Run != nil && journalPath != "" {
			kind, msg := "complete", "Agent completed the task."
			if err != nil {
				kind, msg = "error", textutil.ClipOneLine(err.Error(), 4000)
			}
			_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: kind, Message: msg})
		}

		if e.opts.Run != nil {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Cwd: cacheCwd, Prompt: prompt, Result: resultAny, Error: errStr, Dir: retainedDir, Worktree: worktreeStatus, Workflow: e.workflowID})
		}
		if err != nil {
			emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", DurMs: dur.Milliseconds(), Error: textutil.ClipOneLine(err.Error(), 2000), Dir: retainedDir, Worktree: worktreeStatus})
			reject(err)
			return
		}
		emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "ok", DurMs: dur.Milliseconds(), Preview: runstore.Summary(rawOut), Dir: retainedDir, Worktree: worktreeStatus})
		resolve(resultAny)
	}()

	return vm.ToValue(promise)
}

// workflow implements one nested workflow() call. The child has its own JS
// runtime and phase state, but all paid-work limits live in sharedState.
func (e *engine) workflow(call goja.FunctionCall) goja.Value {
	vm := e.vm
	promise, resolveFn, rejectFn := vm.NewPromise()
	var release func()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) {
			if release != nil {
				release()
				release = nil
			}
			resolveFn(vm.ToValue(v))
		})
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) {
			if release != nil {
				release()
				release = nil
			}
			rejectFn(vm.NewGoError(err))
		})
	}

	if e.depth >= 1 {
		reject(fmt.Errorf("workflow nesting limit exceeded: a nested workflow cannot call workflow() (maximum depth is 1)"))
		return vm.ToValue(promise)
	}
	ref := strings.TrimSpace(call.Argument(0).String())
	path, src, err := e.resolveWorkflow(ref)
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	if e.opts.CheckReplaySources && e.opts.ReplaySources[path] != plan.SourceSHA256(src) {
		sourceErr := fmt.Errorf("nested workflow %q was not captured before the run", ref)
		if e.opts.Replay != nil {
			reject(e.opts.Replay.fail(sourceErr))
			return vm.ToValue(promise)
		}
		if err := runstore.MarkWorkflowManifestUnsafe(e.ctx, e.runID(), sourceErr.Error()); err != nil {
			e.manifestWriteFailed()
			reject(fmt.Errorf("mark nested workflow unsafe: %w", err))
			return vm.ToValue(promise)
		}
	}

	var workflowArgs any
	if encoded := call.Argument(1); !goja.IsUndefined(encoded) {
		if err := json.Unmarshal([]byte(encoded.String()), &workflowArgs); err != nil {
			reject(fmt.Errorf("workflow args must be valid JSON: %w", err))
			return vm.ToValue(promise)
		}
	}
	planNodeID := ""
	if third := call.Argument(2); !goja.IsUndefined(third) && !goja.IsNull(third) {
		planNodeID = strings.TrimSpace(third.String())
	}

	release, err = e.beginReplayAsync()
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	e.shared.mu.Lock()
	e.shared.workflowSeq++
	workflowID := fmt.Sprintf("nested-%d", e.shared.workflowSeq)
	e.shared.mu.Unlock()
	e.phaseMu.Lock()
	parentPhase := e.curPhase
	e.phaseMu.Unlock()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	childPlanID := ""
	if e.opts.Run != nil {
		if err := e.opts.Run.StartWorkflow(workflowID, src); err != nil {
			reject(fmt.Errorf("start nested workflow %q: %w", ref, err))
			return vm.ToValue(promise)
		}
	}
	childOpts := e.opts
	childOpts.ScriptSrc = src
	childOpts.ScriptPath = path
	childOpts.Args = workflowArgs
	prepared, preparedID, planErr := prepareScript(childOpts, workflowID)
	if e.opts.Run != nil {
		childPlanID = preparedID
	}

	e.emit(runstore.Event{
		T: "workflow_start", Workflow: workflowID, Parent: parentRunID(e.opts.Run),
		Title: name, Ref: path, Phase: parentPhase, PlanNodeID: planNodeID,
		WorkflowPlanID: childPlanID,
	})
	if planErr != nil || !e.beginWorker() {
		err := planErr
		if err == nil {
			err = e.ctx.Err()
		}
		if err == nil {
			err = fmt.Errorf("workflow is already finishing")
		}
		e.emit(runstore.Event{
			T: "workflow_end", Workflow: workflowID, Title: name, Phase: parentPhase,
			Status: "error", Error: errorString(err),
		})
		reject(err)
		return vm.ToValue(promise)
	}

	go func() {
		defer e.workers.Done()
		started := time.Now()
		resultJSON, runErr := execute(e.ctx, childOpts, e.shared, e.depth+1, workflowID, &preparedScript{source: prepared, workflowPlanID: preparedID})
		var result any
		if runErr == nil {
			if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
				runErr = fmt.Errorf("nested workflow %q returned invalid JSON: %w", ref, err)
			}
		}
		status := "ok"
		if runErr != nil {
			status = "error"
		}
		if e.opts.Run != nil {
			if finishErr := e.opts.Run.FinishWorkflow(workflowID, resultJSON); finishErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("finish nested workflow %q: %w", ref, finishErr))
				status = "error"
			}
		}
		e.emit(runstore.Event{
			T: "workflow_end", Workflow: workflowID, Title: name, Phase: parentPhase,
			Status: status, DurMs: time.Since(started).Milliseconds(), Error: errorString(runErr),
		})
		if runErr != nil {
			reject(fmt.Errorf("nested workflow %q failed: %w", ref, runErr))
			return
		}
		resolve(result)
	}()

	return vm.ToValue(promise)
}

func parentRunID(run *runstore.Run) string {
	if run == nil {
		return ""
	}
	return run.Meta.ID
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return textutil.ClipOneLine(err.Error(), 2000)
}

func (e *engine) resolveWorkflow(ref string) (string, string, error) {
	return workflowresolve.Resolve(ref, e.opts.ScriptPath, e.opts.WorkDir, e.opts.WorkflowDir)
}

func clampAgentTimeout(timeout time.Duration) time.Duration {
	if timeout < minimumAgentTimeout {
		return minimumAgentTimeout
	}
	return timeout
}

func resolveAgentTimeout(timeout time.Duration, timeoutSet bool, prof profile.Profile) time.Duration {
	if prof.TimeoutSec > 0 && !timeoutSet {
		timeout = time.Duration(prof.TimeoutSec) * time.Second
	}
	return clampAgentTimeout(timeout)
}

func (e *engine) runID() string {
	if e.opts.Run == nil {
		return ""
	}
	return e.opts.Run.Meta.ID
}

func (e *engine) appendWorktreeChanges(agentID int, label, journalPath, worktreeDir, baseCommit string) bool {
	if e.opts.Run == nil || journalPath == "" {
		return false
	}
	entries, err := worktreeGitJournalEntries(context.Background(), worktreeDir, baseCommit)
	if err != nil {
		e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s could not inspect retained worktree changes: %v", label, err))
		return false
	}
	for _, entry := range entries {
		if err := e.opts.Run.AppendAgentJournal(agentID, entry); err != nil {
			e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s could not journal retained worktree changes: %v", label, err))
			return false
		}
	}
	return true
}

func (e *engine) trackRetainedWorktree(rec worktrees.Record) {
	if rec.RunID == "" {
		return
	}
	if rec.Head == "" {
		rec.Head = rec.Base
	}
	if err := worktrees.Track(rec); err != nil {
		e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s could not register retained worktree: %v", rec.Label, err))
		return
	}
	e.shared.mu.Lock()
	e.shared.worktrees = append(e.shared.worktrees, rec)
	e.shared.mu.Unlock()
}

func worktreeGitJournalEntries(ctx context.Context, worktreeDir, baseCommit string) ([]runstore.AgentJournalEntry, error) {
	changes, err := worktrees.Changes(ctx, worktreeDir, baseCommit, worktreeJournalEntryLimit)
	if err != nil {
		return nil, err
	}
	entries := make([]runstore.AgentJournalEntry, 0, len(changes))
	for _, change := range changes {
		message := change.Commit + " " + change.Subject
		if change.Kind == "git-diff" {
			message = change.Action + " " + change.Path
			if change.Insertions != "" && change.Action != "deleted" {
				message += " +" + change.Insertions + "/-" + change.Deletions
			}
		}
		if change.Omitted > 0 {
			noun := "commits"
			if change.Kind == "git-diff" {
				noun = "changed files"
			}
			message = fmt.Sprintf("...and %d more %s", change.Omitted, noun)
		}
		entries = append(entries, runstore.AgentJournalEntry{Kind: change.Kind, Message: worktreeJournalMessage(message)})
	}
	return entries, nil
}

func worktreeJournalMessage(message string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	const maxBytes = 4000
	for len(message) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return message
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func optInt(o map[string]any, key string) (int, bool) {
	if o == nil {
		return 0, false
	}
	v, ok := o[key]
	if !ok || v == nil {
		return 0, false
	}
	f, ok := toFloat(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func prepareScript(o Options, workflowID string) (string, string, error) {
	opts := plan.Options{WriteAncestry: plan.StampAncestry,
		SourcePath: o.ScriptPath, WorkDir: o.WorkDir, WorkflowDir: o.WorkflowDir,
		ResumedFrom: o.ResumeFrom,
	}
	wfPlanID := plan.RootWorkflowID
	dir := ""
	if o.Run != nil {
		if workflowID != "" {
			dir = filepath.Join(o.Run.Dir, "workflows", workflowID)
		} else {
			dir = o.Run.Dir
		}
	}
	if workflowID != "" {
		opts.Depth = 1
		if o.Run != nil {
			parent, err := plan.ReadFile(o.Run.Dir)
			if errors.Is(err, plan.ErrAncestry) {
				return "", wfPlanID, err
			}
			if err == nil {
				if childID, ok := plan.LookupNestedWorkflow(parent, o.ScriptPath); ok {
					wfPlanID = childID
					if dir != "" {
						if _, err := plan.EnsureNested(o.Run.Dir, dir, o.ScriptSrc, o.ScriptPath, opts); err != nil {
							if errors.Is(err, plan.ErrAncestry) {
								return "", wfPlanID, err
							}
							if o.OnWarning != nil {
								o.OnWarning("plan persist: " + err.Error())
							}
						}
					}
					src := o.ScriptSrc
					if parent.Status != plan.StatusUnavailable {
						if instrumented, err := plan.InstrumentWorkflow(o.ScriptSrc, parent, childID); err == nil {
							src = instrumented
						} else if o.OnWarning != nil {
							o.OnWarning("plan instrument failed; using regexp wrap: " + err.Error())
						}
					}
					return src, wfPlanID, nil
				}
			}
		}
		wfPlanID = workflowID
	}
	var p *plan.Plan
	if dir != "" {
		var err error
		p, err = plan.Ensure(dir, o.ScriptSrc, opts)
		if errors.Is(err, plan.ErrAncestry) {
			return "", wfPlanID, err
		}
		if err != nil && o.OnWarning != nil {
			o.OnWarning("plan persist: " + err.Error())
		}
	}
	if p == nil {
		p = plan.Extract(o.ScriptSrc, opts)
	}
	src := o.ScriptSrc
	if p != nil && p.Status != plan.StatusUnavailable {
		if instrumented, err := plan.Instrument(o.ScriptSrc, p); err == nil {
			src = instrumented
		} else if o.OnWarning != nil {
			o.OnWarning("plan instrument failed; using regexp wrap: " + err.Error())
		}
	}
	return src, wfPlanID, nil
}

func (e *engine) snapshotCallPlan(nodeID string) callPlan {
	cp := callPlan{nodeID: nodeID, wfPlanID: e.workflowPlanID}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	if e.planGroupID != "" {
		cp.groupID = e.planGroupID
	}
	if e.planIndexSet {
		s, i := e.planStage, e.planItem
		cp.stage, cp.item = &s, &i
	}
	return cp
}

// transform rewrites `export const meta = {...}` into a global assignment and
// wraps the whole script in an async IIFE whose settlement finishes the run.
func transform(src string) (string, error) {
	src = metaRe.ReplaceAllString(src, "const meta = globalThis.__meta =")
	return "(async () => {\n" + src + "\n})().then(" +
		"r => __finish(JSON.stringify(r === undefined ? null : r) ?? 'null'))" +
		".catch(e => __fail(String((e && e.stack) || e)));", nil
}

var metaRe = regexp.MustCompile(`(?m)^\s*export\s+const\s+meta\s*=`)

var resumeNondeterminismPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{name: "Date.now()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])Date\s*\.\s*now\s*\(`)},
	{name: "new Date()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])new\s+Date\s*\(`)},
	{name: "Math.random()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])Math\s*\.\s*random\s*\(`)},
}

func resumeNondeterminismWarning(src string) string {
	var found []string
	for _, pattern := range resumeNondeterminismPatterns {
		if pattern.re.MatchString(src) {
			found = append(found, pattern.name)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("workflow uses resume-unstable JavaScript APIs (%s); --resume cache hits may not work as expected for agent() calls whose prompt or schema depends on those values; pass timestamps or random seeds through args instead", strings.Join(found, ", "))
}

// cachedSummary is the one-line preview of a replayed result.
func cachedSummary(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return textutil.ClipOneLine(fmt.Sprintf("%v", v), 200)
	}
	return runstore.Summary(string(b))
}

// slotBlocked observes only the path that must wait. Tests use it to release
// a running agent after another agent reaches the shared slot boundary.
var slotBlocked = func() {}

func acquireSlot(ctx context.Context, slots chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case slots <- struct{}{}:
		return nil
	default:
	}
	slotBlocked()
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
