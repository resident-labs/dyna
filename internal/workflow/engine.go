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
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/console"
	"github.com/dop251/goja_nodejs/eventloop"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/harness"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/planclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/structuredoutput"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workerprompt"
	"dyna-agent/internal/workflowresolve"
	"dyna-agent/internal/worktrees"
)

const (
	minimumAgentTimeout       = 30 * time.Minute
	defaultAgentTimeout       = 5 * time.Hour
	defaultJournalIdle        = 20 * time.Minute
	worktreeJournalEntryLimit = 20
)

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
	Poster func(ctx context.Context, runID string, mentions []string, text string) (PostResult, error)
	// JournalIdle controls how long a running worker may go without an
	// agent-authored progress record before an exact-session reminder. Zero
	// uses twenty minutes; exposed primarily as a deterministic test seam.
	JournalIdle time.Duration
	// MaxToolWait bounds how long a steering message or journal reminder waits
	// for an in-flight tool call (steering.maxToolWait). Zero reads settings.json
	// and falls back to two minutes.
	MaxToolWait time.Duration
	// Paused, when it returns true, blocks new worker launches (running
	// workers finish). Polled while waiting to start each agent.
	Paused func() bool
	// ResumeFrom is the prior run id when this execution is a --resume.
	// Recorded on plan.json as resumedFrom; it does not change cache keys.
	ResumeFrom string
}

type outcome struct {
	resultJSON string
	err        error
}

// Execute runs the script to completion and returns the workflow's return
// value as JSON.
func Execute(ctx context.Context, o Options) (string, error) {
	if o.Run != nil && o.Store != nil {
		profiles := make([]workeroutput.ProfileSnapshot, 0, len(o.Store.Profiles))
		for _, p := range o.Store.Profiles {
			profiles = append(profiles, workeroutput.ProfileSnapshot{
				Name: p.Name, Model: p.Model, Output: p.EffectiveOutput(),
			})
		}
		if err := o.Run.SnapshotOutputProfiles(profiles); err != nil {
			o.Run.Append(runstore.Event{
				T: "log", Msg: fmt.Sprintf("output profile snapshot unavailable: %v", err),
				Provenance: runstore.LogProvenanceSystem,
			})
		}
	}
	return execute(ctx, o, nil, 0, "", nil)
}

// sharedState owns every limit whose meaning is "per run", including calls
// made by nested workflows. Each script still gets an isolated JS runtime.
type sharedState struct {
	sem chan struct{}

	mu                sync.Mutex
	scratchID         string
	scratchWarning    sync.Once
	agentSeq          int
	workflowSeq       int
	profSems          map[string]chan struct{}
	profCalls         map[string]int
	capErr            error // first profile call-cap failure anywhere in this run's tree
	abortRoot         func(error)
	worktreeRetention string
	worktrees         []worktrees.Record
}

type preparedScript struct {
	source, workflowPlanID string
}

func execute(ctx context.Context, o Options, shared *sharedState, depth int, workflowID string, prepared *preparedScript) (string, error) {
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
		worktreeRetention, warning, err := resolveWorktreeRetention(o.WorktreeRetention)
		if err != nil {
			return "", err
		}
		worktreeWarning = warning
		shared = &sharedState{
			scratchID: runstore.NewID(),
			sem:       make(chan struct{}, maxConc),
			profSems:  map[string]chan struct{}{}, profCalls: map[string]int{}, worktreeRetention: worktreeRetention,
		}
	}
	if warning := resumeNondeterminismWarning(o.ScriptSrc); warning != "" && o.OnWarning != nil {
		o.OnWarning(warning)
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
	workerMu       sync.Mutex
	workers        sync.WaitGroup
	stopping       bool
	planMu         sync.Mutex
	planGroupID    string
	planStage      int
	planItem       int
	planIndexSet   bool
}

type callPlan struct {
	nodeID, groupID, wfPlanID string
	stage, item               *int
}

// settle delivers the run's single outcome (first caller wins).
func (e *engine) settle(o outcome) {
	e.settleOnce.Do(func() { e.done <- o })
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
}

func (e *engine) emit(ev runstore.Event) {
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
		Msg:        truncate(msg, 4000),
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
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
	}

	prompt := call.Argument(0).String()
	opts := call.Argument(1)

	profName, label, phase, cwd, planNodeID := "", "", "", e.opts.WorkDir, ""
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
		e.emit(ev)
	}
	emitA(runstore.Event{T: "agent_start", ID: id, Label: label, Profile: prof.Name, Phase: phase, Preview: truncate(prompt, 160)})

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
		emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(err.Error(), 2000)})
		reject(err)
		return vm.ToValue(promise)
	}
	journalPath := ""
	failBeforeRun := func(err error) {
		if e.opts.Run != nil && journalPath != "" {
			_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "error", Message: truncate(err.Error(), 4000)})
		}
		emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(err.Error(), 2000)})
		reject(err)
	}
	go func() {
		defer e.workers.Done()
		prof.Env = cloneEnv(prof.Env)
		childenv.SetTrimpath(prof.Env)
		prof.Env[mode.EnvMode] = mode.Workflow
		prof.Env[mode.EnvWorkerAgent] = strconv.Itoa(id)
		scratchID := e.shared.scratchID
		if e.opts.Run != nil {
			scratchID = e.opts.Run.Meta.ID
		}
		scratchPath, scratchErr := scratch.Ensure(fmt.Sprintf("%s/agent-%d", scratchID, id))
		if scratchErr != nil {
			e.shared.scratchWarning.Do(func() { log.Printf("workflow scratch unavailable: %v", scratchErr) })
		}
		prof.Env[scratch.Env] = scratchPath
		if e.opts.Run != nil {
			prof.Env[mode.EnvWorkerRun] = e.opts.Run.Meta.ID
			prof.Env[mode.EnvRoomID] = e.opts.Run.Meta.RoomID
			prof.Env[mode.EnvRoom] = e.opts.Run.Meta.Room
			prof.Env[mode.EnvAddress] = fmt.Sprintf("run:%s/agent:%d", e.opts.Run.Meta.ID, id)
		}
		// The journal begins when the engine accepts the call, before any local
		// execution queue. A workflow that finishes while this worker is
		// still queued therefore retains a terminal, resume-safe record.
		if e.opts.Run != nil {
			var journalErr error
			journalPath, journalErr = e.opts.Run.StartAgentJournal(id, label, prof.Name, phase, prompt)
			if journalErr != nil {
				emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(journalErr.Error(), 2000)})
				reject(fmt.Errorf("start work journal for %s: %w", label, journalErr))
				return
			}
			prof.Env = cloneEnv(prof.Env)
			prof.Env[runstore.AgentJournalEnv] = journalPath
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
		emitA(runstore.Event{T: "agent_run", ID: id, Label: label, Profile: prof.Name, Phase: phase})
		start := time.Now()

		actx, cancel := context.WithTimeout(e.ctx, timeout)
		defer cancel()

		outputPath := ""
		normalizedOutputPath := ""
		if e.opts.Run != nil {
			var journalErr error
			outputPath, journalErr = e.opts.Run.StartAgentOutput(id)
			if journalErr != nil {
				e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s live harness output unavailable: %v", label, journalErr))
				outputPath = ""
			}
			normalizedOutputPath, journalErr = e.opts.Run.StartAgentNormalizedOutput(id, workeroutput.ProfileSnapshot{
				Name: prof.Name, Model: prof.Model, Output: prof.EffectiveOutput(),
			})
			if journalErr != nil {
				e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s normalized output unavailable: %v", label, journalErr))
				normalizedOutputPath = ""
			}
			if runRoot, rootErr := filepath.Abs(e.opts.Run.Dir); rootErr == nil {
				prof.Env[runstore.AgentJournalRootEnv] = runRoot
			}
			if exe, exeErr := fsutil.RunningExecutable(); exeErr == nil {
				prof.Env["DYNA_BIN"] = exe
			}
		}

		// Worktree isolation: run the worker on a detached copy of the repo and
		// apply the resolved per-call retention when it finishes.
		retainedDir := ""
		worktreeStatus := ""
		worktreeBase := ""
		worktreeRepo := ""
		var cleanupWT func() bool
		if isolation == "worktree" {
			worktreeRepo, _ = filepath.Abs(cwd)
			if root, rootErr := worktrees.RepositoryRoot(actx, cwd); rootErr == nil {
				worktreeRepo, _ = filepath.Abs(root)
			}
			owner := os.Getenv("DYNA_SESSION")
			if e.opts.Run != nil && e.opts.Run.Meta.ParentSession != "" {
				owner = e.opts.Run.Meta.ParentSession
			}
			wt, baseCommit, cleanup, werr := worktrees.CreateForRun(actx, cwd, worktreeRetention, e.runID(), owner)
			if werr != nil {
				if e.opts.Run != nil && journalPath != "" {
					_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "error", Message: truncate(werr.Error(), 4000)})
				}
				emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(werr.Error(), 2000)})
				reject(werr)
				return
			}
			cwd = wt
			worktreeBase = baseCommit
			cleanupWT = cleanup
		}
		maps.Copy(prof.Env, worktrees.PortEnvForDir(actx, cwd))

		journalIdle := e.opts.JournalIdle
		if journalIdle <= 0 {
			journalIdle = harness.JournalIdleFromEnv(defaultJournalIdle)
		}
		journalOpts := harness.JournalOptions{
			Path: journalPath, OutputPath: outputPath, NormalizedOutputPath: normalizedOutputPath,
			Output: prof.EffectiveOutput(), IdleAfter: journalIdle, State: harness.NewJournalState(),
			MaxToolWait: e.maxToolWait(),
			OnDelivery: func(note harness.DeliveryNote) {
				e.recordDelivery(emitA, id, label, prof.Name, phase, journalPath, note)
			},
		}
		steeringOpts := harness.SteeringOptions{}
		if journalPath != "" {
			nudgeSent := make(map[string]bool)
			journalOpts.OnEntry = func(entry runstore.AgentJournalEntry) {
				nudgeSent[harness.JournalNudgeIdle] = false
				emitA(runstore.Event{
					T: "agent_journal", ID: id, Label: label, Profile: prof.Name, Phase: phase,
					Kind: entry.Kind, Preview: truncate(entry.Message, 240),
				})
			}
			journalOpts.OnNudge = func(nudge harness.JournalNudge) {
				wasSent := nudgeSent[nudge.Reason]
				if nudge.Delivered {
					nudgeSent[nudge.Reason] = true
				}
				msg := "journal quiet for twenty minutes; exact-session write-and-continue reminder sent"
				next := "Write a progress entry, then continue the original task."
				status := "sent"
				if nudge.Reason == harness.JournalNudgeMissing {
					msg = "worker finished without an agent-authored entry; exact-session write-now reminder sent"
					next = "Write one brief entry; preserve the completed task result."
				}
				if !nudge.Delivered {
					status = "unavailable"
					if wasSent {
						status = "ignored"
						msg = "exact-session journal reminder was delivered, but the worker still wrote no agent-authored entry"
						next = "No further reminder will be sent for this completed task."
					} else if nudge.Reason == harness.JournalNudgeMissing {
						msg = "worker finished without an agent-authored entry; a safe exact-session reminder was unavailable or failed"
					} else {
						msg = "journal quiet for twenty minutes; live reminder unavailable because this harness/session cannot be safely resumed"
					}
				}
				_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{
					Kind: "nudge", Message: msg, Next: next,
				})
				emitA(runstore.Event{
					T: "agent_nudge", ID: id, Label: label, Profile: prof.Name, Phase: phase,
					Kind: nudge.Reason, Status: status, Msg: msg,
				})
			}
			steeringOpts = harness.SteeringOptions{
				RunID: e.opts.Run.Meta.ID, AgentID: id,
				OnDispatch: func(message runstore.SteeringMessage) error {
					return e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{
						Kind: "steer", Message: message.Message,
					})
				},
				OnMessage: func(message runstore.SteeringMessage) {
					emitA(runstore.Event{
						T: "agent_steer", ID: id, Label: label, Profile: prof.Name, Phase: phase,
						Status: "delivered", Msg: truncate(message.Message, 2000), Preview: truncate(message.Message, 240),
					})
				},
			}
		}
		// A worker runs one conversation, so its instructions are built once.
		standing := e.workerInstructions(journalPath != "")
		workerPrompt := workerTaskPrompt(prompt, journalPath)
		prof.RuntimeInstructions = standing.Text()
		workerPrompt = harness.WithPromptInstructions(prof, workerPrompt)

		var (
			resultAny any
			rawOut    string
			nudged    bool
			err       error
		)
		if schemaJSON != "" {
			resultAny, rawOut, nudged, err = e.runWithSchema(actx, prof, workerPrompt, schemaJSON, cwd, journalOpts, steeringOpts, e.agentExecution(id))
		} else {
			var r harness.Result
			r, err = harness.RunWithOptions(actx, prof, workerPrompt, cwd, true, journalOpts, steeringOpts, harness.SessionOptions{}, e.agentExecution(id))
			rawOut = r.Output
			resultAny = r.Output
			nudged = r.Nudged
		}
		dur := time.Since(start)
		if nudged && err == nil {
			e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s recovered by nudging the same %s session", label, prof.Harness))
		}

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
				kind, msg = "error", truncate(err.Error(), 4000)
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
			emitA(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", DurMs: dur.Milliseconds(), Error: truncate(err.Error(), 2000), Dir: retainedDir, Worktree: worktreeStatus})
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
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
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
	return truncate(err.Error(), 2000)
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

// runWithSchema wraps the prompt with JSON-output instructions, extracts and
// validates the JSON, retrying up to 2 times with the validation error in the
// same session. Without exact resume support it retries the full prompt.
// agentExecution runs agent id's harness as a leaf, and records each of its
// processes for the agent number, so the engine tells a run's agents apart
// (requirements item 3). A script run without a run record records nothing.
func (e *engine) agentExecution(id int) harness.ExecutionOptions {
	execution := harness.ExecutionOptions{DisableNativeSubagents: true}
	if e.opts.Run == nil {
		return execution
	}
	run := e.opts.Run.Meta.ID
	execution.Started = func(pid int) {
		launch := callerid.Launch{DataDir: store.DataDir(), Name: run, Actor: callerid.Identity{Kind: callerid.Worker, Run: run, Agent: id}, Launcher: "workflow"}
		if err := callerid.RecordStarted(e.ctx, pid, launch); err != nil {
			log.Printf("workflow: record agent %d of run %s: %v", id, run, err)
		}
	}
	return execution
}

func (e *engine) runWithSchema(ctx context.Context, prof profile.Profile, prompt, schemaJSON, cwd string, journal harness.JournalOptions, steering harness.SteeringOptions, execution harness.ExecutionOptions) (any, string, bool, error) {
	schema, err := structuredoutput.Compile(schemaJSON)
	if err != nil {
		return nil, "", false, err
	}
	base := workerprompt.AppendPromptSuffix(prompt, structuredoutput.SchemaSuffix(schemaJSON))
	ask := base
	var lastErr error
	var raw string
	var nudged bool
	var session harness.SessionOptions
	session.OnRuntime = func(pin harnessruntime.Pin) error { session.RuntimePin = &pin; return nil }
	resumable := harness.Resumable(prof)
	for attempt := 0; attempt < structuredoutput.SchemaAttempts; attempt++ {
		r, err := harness.RunWithOptions(ctx, prof, ask, cwd, !nudged, journal, steering, session, execution)
		nudged = nudged || r.Nudged
		if err != nil {
			return nil, r.Output, nudged, err
		}
		raw = r.Output
		val, jerr := schema.Validate(raw)
		if jerr == nil {
			return val, raw, nudged, nil
		}
		lastErr = jerr
		if resumable && r.SessionID != "" {
			session.ResumeID = r.SessionID
			ask = structuredoutput.SchemaCorrection(jerr, raw, false)
		} else {
			session.ResumeID = ""
			ask = workerprompt.AppendPromptSuffix(base, "\n\n"+structuredoutput.SchemaCorrection(jerr, raw, true))
		}
	}
	return nil, raw, nudged, fmt.Errorf("schema validation failed after 3 attempts: %w", lastErr)
}

func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+2)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// journalBoundary is the standing text of a journaling workflow worker.
const journalBoundary = `[DYNA WORK JOURNAL - REQUIRED]
Keep a concise, append-only work journal while you perform this task. Its run-owned path is in the worker context at the end of the task prompt.

After orienting, append your first entry. Add another after meaningful discoveries, decisions, verification, or blockers; before a long operation; and before finishing. Use:
  dyna journal "what changed or what you learned" --kind update --next "next concrete step"
If dyna is not on PATH, invoke the current binary as "$DYNA_BIN" journal ... instead.
Kinds include update, finding, decision, verification, and blocker. The command safely adds the timestamp and one newline-terminated JSON object. Keep each entry brief: one or two sentences plus an optional next step. Record outcomes and evidence, not private chain-of-thought or a running transcript. Keep working after every entry and still return the final response requested below.

Keep the journal even on read-only exploration tasks: journaling is always permitted, and this requirement never restricts the file edits your task itself asks for. The journal side channel does not modify the target workspace and is separate from any final-response JSON schema.
[/DYNA WORK JOURNAL]

[DYNA WORKER BOUNDARY]
You are running INSIDE a dyna workflow as a worker (workflow mode), and you are a leaf: complete the task yourself. Delegation is disabled: do not use subagents, child agents, or your harness's Agent/multi-agent tools, and do not load the dyna skill or start anything with dyna. dyna run and dyna subagent fail inside a workflow worker. The only dyna commands you may use are dyna journal as described above and read-only inspection (dyna runs list/show/output).
[/DYNA WORKER BOUNDARY]`

// workflowRestriction is the standing text of every workflow worker.
const workflowRestriction = `[DYNA WORKFLOW RESTRICTION]
Put throwaway files (screenshots, cards, scripts, notes) in $DYNA_SCRATCH, never in a repository.
You must complete this task yourself. Do not spawn, delegate to, or invoke any subagent, child agent, or other agent, and do not start dyna runs or subagents. This restriction overrides any contrary instruction in the task.
[/DYNA WORKFLOW RESTRICTION]`

// workerInstructions is a workflow worker's standing instructions: the work
// journal when it keeps one, the workflow restriction, and the global and room
// rules, from the same pieces sessions use.
func (e *engine) workerInstructions(journal bool) instructions.Snapshot {
	parts := []instructions.Section{{Name: "Workflow restriction", Text: workflowRestriction}}
	if journal {
		parts = append([]instructions.Section{{Name: "Work journal", Text: journalBoundary}}, parts...)
	}
	roomID := ""
	if e.opts.Run != nil {
		roomID = e.opts.Run.Meta.RoomID
	}
	return instructions.Build(append(parts, goals.DefaultStore().InstructionSections(roomID)...)...)
}

// workerTaskPrompt is the task, with the run-owned journal path in a trailing
// worker context when the worker keeps a journal.
func workerTaskPrompt(task, journalPath string) string {
	prompt := "[ORIGINAL TASK]\n" + task
	if journalPath == "" {
		return prompt
	}
	return workerprompt.WithWorkerContext(prompt, "Run-owned journal path:\n"+journalPath)
}

// maxToolWait resolves steering.maxToolWait once per engine.
func (e *engine) maxToolWait() time.Duration {
	if e.opts.MaxToolWait > 0 {
		return e.opts.MaxToolWait
	}
	if v, err := settings.Load(settings.DefaultPath()); err == nil && v.SteeringMaxToolWait > 0 {
		return v.SteeringMaxToolWait
	}
	return harness.DefaultMaxToolWait
}

// recordDelivery writes a deferred (or undeferrable) steering/journal delivery
// to the run's event log, and to the agent journal when it interrupted a
// worker whose output shows no tool boundaries.
func (e *engine) recordDelivery(emit func(runstore.Event), id int, label, profileName, phase, journalPath string, note harness.DeliveryNote) {
	var msg string
	switch note.Outcome {
	case harness.DeliveryBoundary:
		msg = fmt.Sprintf("%s delivery waited %s for an in-flight tool call to finish", note.Kind, note.Waited.Round(time.Millisecond))
	case harness.DeliveryTurnEnd:
		msg = fmt.Sprintf("%s delivery waited %s and was delivered as the next continuation because the turn ended first", note.Kind, note.Waited.Round(time.Millisecond))
	case harness.DeliveryUnsupported:
		msg = fmt.Sprintf("%s delivery waited %s; this profile's output dialect exposes no tool-call boundaries, so the worker was interrupted after steering.maxToolWait", note.Kind, note.Waited.Round(time.Millisecond))
	default:
		return
	}
	emit(runstore.Event{
		T: "agent_defer", ID: id, Label: label, Profile: profileName, Phase: phase,
		Kind: note.Kind, Status: note.Outcome, DurMs: note.Waited.Milliseconds(), Msg: msg,
	})
	if e.opts.Run != nil && journalPath != "" && note.Outcome == harness.DeliveryUnsupported {
		_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "delivery", Message: truncate(msg, 400)})
	}
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

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace for one-line previews
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func prepareScript(o Options, workflowID string) (string, string, error) {
	opts := plan.Options{WriteAncestry: planclient.StampAncestry,
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
		return truncate(fmt.Sprintf("%v", v), 200)
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
