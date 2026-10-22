// Package workflowsvc runs and launches workflows for CLI and app callers.
package workflowsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/planclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
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

// Execute owns the run lifecycle, including final status and completion delivery.
func Execute(ctx context.Context, opts Options, store *profile.Store, observer Observer) (Result, error) {
	if err := authority.Check("run", authority.Current(), nil); err != nil {
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
	run, err := runstore.Create(opts.Name, opts.ScriptSrc, opts.Args)
	if err != nil {
		return Result{}, err
	}
	if _, planErr := plan.Ensure(run.Dir, opts.ScriptSrc, plan.Options{WriteAncestry: planclient.StampAncestry, SourcePath: opts.ScriptPath, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir, ResumedFrom: opts.ResumeFrom}); planErr != nil {
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
	if err == nil {
		value, err = workflow.Execute(ctx, workflow.Options{
			ScriptSrc: opts.ScriptSrc, ScriptPath: opts.ScriptPath, Args: opts.Args, Store: store, Run: run,
			OnEvent: observer.OnEvent, OnWarning: warning, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir,
			MaxConc: opts.MaxConc, MaxAgents: opts.MaxAgents, Cache: cache, ResumeFrom: opts.ResumeFrom,
			Paused: func() bool { return runstore.IsPaused(run.Meta.ID) }, Poster: ScriptPoster,
		})
	}
	_ = runstore.SetPaused(run.Meta.ID, false)
	status := "ok"
	if err != nil {
		if ctx.Err() != nil {
			status = "canceled"
		} else {
			status = "error"
		}
	}
	run.Append(runstore.Event{T: "run_end", Status: status, DurMs: time.Since(start).Milliseconds()})
	run.Finish(status, value, err)
	if run.Meta.Detached {
		if _, nerr := notify.Default().CompleteRun(context.Background(), run.Meta.ID); nerr != nil {
			warning("completion message: " + nerr.Error())
		}
	}
	if cache != nil && observer.OnCache != nil {
		observer.OnCache(cache.Stats())
	}
	return Result{ID: run.Meta.ID, Status: status, Value: value}, err
}
