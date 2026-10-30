// Package workflowsvc runs and launches workflows for CLI and app callers.
package workflowsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	datastore "dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workflow"
)

// Options contains the workflow inputs shared by foreground and detached runs.
type Options struct {
	ScriptSrc   string
	ScriptPath  string
	Name        string
	Args        any
	WorkDir     string
	WorkflowDir string
	ResumeFrom  string
	MaxConc     int
	MaxAgents   int
	// MaxCommands caps the run() commands that execute at once (0 => 4).
	MaxCommands int
	// Demo runs the script on the unsaved mock worker profiles instead of the
	// saved registry. Only `dyna demo` sets it.
	Demo bool
}

// Observer receives progress without choosing a terminal or app presentation.
type Observer struct {
	OnEvent   func(runstore.Event)
	OnWarning func(string)
	OnStart   func(runstore.Meta)
	OnResume  func(int)
	OnCache   func(workflow.CacheStats)
}

type Result struct {
	ID     string
	Status string
	Value  string
}

// AgentEngine connects a run coordinator to the engine, which starts the leaf
// session of each agent() call. Tests replace it with a fake engine.
var AgentEngine = func() workflow.EngineCaller { return engineclient.New("") }

// Execute owns the run lifecycle, including final status and completion delivery.
func Execute(ctx context.Context, opts Options, store *profile.Store, observer Observer) (Result, error) {
	return executeWithOutbox(ctx, opts, store, observer, newCoordinatorOutbox)
}

func executeWithOutbox(ctx context.Context, opts Options, store *profile.Store, observer Observer, factory func(string) coordinatorOutbox) (Result, error) {
	actor := authority.Current()
	if err := authority.Check("workflow run", actor, nil); err != nil {
		return Result{}, err
	}
	// Every run path funnels here with its caller's stamps (a session's shell,
	// the app's StartRun, `dyna demo`), so this one check refuses a leaf
	// session's run wherever it was launched.
	if err := subagent.RefuseLeafStart(actor); err != nil {
		return Result{}, err
	}
	if store == nil {
		var err error
		store, err = profile.Load(profile.DefaultPath())
		if err != nil {
			return Result{}, err
		}
	}
	if len(store.Profiles) == 0 {
		return Result{}, fmt.Errorf("no worker profiles registered; run `dyna profiles add` or `dyna demo` first")
	}
	if opts.Name == "" {
		opts.Name = MetaName(opts.ScriptSrc, opts.ScriptPath)
	}
	warning := func(message string) {
		if observer.OnWarning != nil {
			observer.OnWarning(message)
		}
	}
	var cache *workflow.Cache
	if opts.ResumeFrom != "" {
		entries, err := runstore.ReadJournal(opts.ResumeFrom)
		if err != nil {
			return Result{}, fmt.Errorf("cannot resume %s: %w", opts.ResumeFrom, err)
		}
		cache = workflow.NewCache(entries)
		if observer.OnResume != nil {
			observer.OnResume(len(entries))
		}
	}
	// Every resume in one lineage uses the original run as its job namespace.
	// This keeps idempotency keys stable even when no journal entry survived.
	jobNamespace := opts.ResumeFrom
	seen := map[string]bool{}
	for jobNamespace != "" && !seen[jobNamespace] {
		seen[jobNamespace] = true
		parent, err := runstore.ResumedFrom(jobNamespace)
		if err != nil {
			return Result{}, fmt.Errorf("resolve job resume identity: %w", err)
		}
		if parent == "" {
			break
		}
		jobNamespace = parent
	}
	run, err := runstore.Create(opts.Name, opts.ScriptSrc, opts.Args)
	if err != nil {
		return Result{}, err
	}
	if jobNamespace == "" {
		jobNamespace = run.Meta.ID
	}
	prefix := fmt.Sprintf("%s/%d/%d", run.Meta.ID, os.Getpid(), time.Now().UnixNano())
	defer installRunWriters(ctx, run, factory(prefix))()
	if _, planErr := plan.Ensure(run.Dir, opts.ScriptSrc, plan.Options{WriteAncestry: plan.StampAncestry, SourcePath: opts.ScriptPath, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir, ResumedFrom: opts.ResumeFrom}); planErr != nil {
		if errors.Is(planErr, plan.ErrAncestry) {
			err = planErr
		} else {
			warning("plan persist: " + planErr.Error())
		}
	}
	if observer.OnStart != nil {
		observer.OnStart(run.Meta)
	}
	run.Append(runstore.Event{T: "run_start", Title: opts.Name})
	start := time.Now()
	var value string
	if err == nil {
		err = ctx.Err()
	}
	agents := AgentEngine()
	var commands workflow.CommandRunner
	if err == nil {
		commands, err = CommandRunnerFor(run, opts.ResumeFrom)
	}
	if err == nil {
		value, err = workflow.Execute(ctx, workflow.Options{
			ScriptSrc: opts.ScriptSrc, ScriptPath: opts.ScriptPath, Args: opts.Args, Store: store, Run: run,
			OnEvent: observer.OnEvent, OnWarning: warning, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir,
			MaxConc: opts.MaxConc, MaxAgents: opts.MaxAgents, MaxCommands: opts.MaxCommands, Commands: commands, Cache: cache, ResumeFrom: opts.ResumeFrom, JobNamespace: jobNamespace, Engine: agents,
			Paused: func() bool { return runstore.IsPaused(run.Meta.ID) }, Poster: ScriptPoster,
		})
	}
	_ = runstore.SetPaused(run.Meta.ID, false)
	status := "ok"
	stopped := workflow.StoppedForUpdate(err)
	if err != nil {
		switch {
		case stopped:
			status = runstore.StatusInterrupted
		case ctx.Err() != nil:
			status = "canceled"
		default:
			status = "error"
		}
	}
	duration := time.Since(start)
	err = errors.Join(err, run.Finish(status, value, err))
	finished := runstore.IsFinishedStatus(run.Meta.Status) && !run.Meta.EndedAt.IsZero()
	if finished {
		status = run.Meta.Status
		if status != "ok" && err == nil {
			err = fmt.Errorf("run finished %s: %s", status, run.Meta.Error)
		}
	} else {
		status = "error"
	}
	run.Append(runstore.Event{T: "run_end", Status: status, DurMs: duration.Milliseconds()})
	// The engine reports an update interruption because this runner may no
	// longer be able to read the database after the update.
	if finished && run.Meta.Detached && !stopped {
		if _, nerr := notify.Default().CompleteRun(context.Background(), run.Meta.ID); nerr != nil {
			warning("completion message: " + nerr.Error())
		}
	}
	if cache != nil && observer.OnCache != nil {
		observer.OnCache(cache.Stats())
	}
	return Result{ID: run.Meta.ID, Status: status, Value: value}, err
}

// coordinatorOutbox keeps directory writes and terminal acknowledgement ordered.
// Native admission uses a separate path until the old #1834 integration is ported.
type coordinatorOutbox interface {
	CallResultWithinID(context.Context, any, string, ...any) (string, error)
	CallResult(context.Context, any, string, ...any) error
	ArmTerminal(context.Context)
	Close(context.Context)
}

func newCoordinatorOutbox(prefix string) coordinatorOutbox {
	out := engineclient.NewOutbox(engineclient.New(""), prefix)
	dataDir := datastore.DataDir()
	out.Spool = func(queued []engineclient.Queued) error {
		db, err := database.Open(dataDir)
		if err != nil {
			return err
		}
		runID := strings.SplitN(prefix, "/", 2)[0]
		return engineclient.AppendRunnerSpool(context.Background(), state.For(db), dataDir, runID, queued)
	}
	return out
}

// installRunWriters runs before plan or workflow artifacts can name directories.
func installRunWriters(ctx context.Context, run *runstore.Run, out coordinatorOutbox) func() {
	var once sync.Once
	var terminal context.Context
	var stop context.CancelFunc
	arm := func() context.Context {
		once.Do(func() {
			terminal, stop = context.WithTimeout(context.Background(), 10*time.Minute)
			out.ArmTerminal(terminal)
		})
		return terminal
	}
	run.SetWriters(func(dirs []string) error {
		_, err := out.CallResultWithinID(ctx, nil, "AddRunDirectories", callspec.RunDirectories{Run: run.Meta.ID, Directories: dirs})
		return err
	}, func(in callspec.RunFinish) (runstore.Meta, error) {
		arm()
		var meta runstore.Meta
		err := out.CallResult(context.WithoutCancel(ctx), &meta, "FinishRun", in)
		if err == nil && (meta.ID != in.Run || !runstore.IsFinishedStatus(meta.Status) || meta.EndedAt.IsZero()) {
			err = errors.New("engine did not acknowledge the run's terminal state")
		}
		return meta, err
	})
	return func() { terminal := arm(); defer stop(); out.Close(terminal) }
}
