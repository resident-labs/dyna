package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/textutil"
)

const jobCancelWait = 10 * time.Second

// jobCallSpec is the JavaScript boundary of job(spec). Durations use the same
// Go duration syntax as dyna job run, such as "30m" or "45s".
type jobCallSpec struct {
	Command   []string `json:"command"`
	Node      string   `json:"node"`
	Labels    []string `json:"labels"`
	Commit    string   `json:"commit"`
	CPUs      int      `json:"cpus"`
	Memory    string   `json:"memory"`
	Workspace bool     `json:"workspace"`
	Timeout   string   `json:"timeout"`
	Idle      string   `json:"idle"`
	Image     string   `json:"image"`
	Host      bool     `json:"host"`
	Artifacts []string `json:"artifacts"`
}

// parseJobCall validates the script value once and returns the typed request
// used by the existing RunJob engine API.
func parseJobCall(call goja.FunctionCall, workDir string) (callspec.RunJobInput, error) {
	var out callspec.RunJobInput
	encoded, err := json.Marshal(call.Argument(0).Export())
	if err != nil {
		return out, fmt.Errorf("job() spec must be JSON: %w", err)
	}
	var in jobCallSpec
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return out, fmt.Errorf("job() spec: %w", err)
	}
	if len(in.Command) == 0 || strings.TrimSpace(in.Command[0]) == "" {
		return out, errors.New("job() needs a command array with a program name")
	}
	var timeout time.Duration
	if in.Timeout != "" {
		timeout, err = time.ParseDuration(in.Timeout)
		if err != nil {
			return out, fmt.Errorf("job() timeout: %w", err)
		}
	}
	var idle *int
	if in.Idle != "" {
		duration, err := time.ParseDuration(in.Idle)
		if err != nil {
			return out, fmt.Errorf("job() idle: %w", err)
		}
		seconds := int(duration / time.Second)
		idle = &seconds
	}
	repo := ""
	if in.Commit != "" {
		repo = workDir
	}
	return callspec.RunJobInput{
		Node: in.Node, Labels: in.Labels, Command: in.Command,
		Repo: repo, Ref: in.Commit, Host: in.Host, Image: in.Image,
		CPUs: in.CPUs, Memory: in.Memory, Timeout: int(timeout / time.Second), Idle: idle,
		Artifacts: in.Artifacts, Workspace: in.Workspace,
	}, nil
}

func jobKey(spec callspec.RunJobInput) string {
	payload, _ := json.Marshal(spec)
	sum := sha256.Sum256(payload)
	return "j1:" + hex.EncodeToString(sum[:])
}

// jobRecord is what a resumed run knows of one earlier job() call.
type jobRecord struct {
	Job    string
	Done   bool
	Result any
}

// jobResult is the value a job() promise resolves with. Log matches the log
// pointer printed by dyna job show.
type jobResult struct {
	ID       string     `json:"id"`
	Node     string     `json:"node"`
	State    jobs.State `json:"state"`
	ExitCode *int       `json:"exitCode"`
	Log      string     `json:"log"`
}

func resultForJob(job jobs.Job) jobResult {
	return jobResult{
		ID: job.ID, Node: job.Node, State: job.State, ExitCode: job.ExitCode,
		Log: "dyna job logs " + job.ID,
	}
}

// job implements one job() call through the engine's job API. The journal
// records the submitted ID before the wait, so resume never submits that call
// again, including when its earlier wait ended in an error.
func (e *engine) job(call goja.FunctionCall) goja.Value {
	vm := e.vm
	promise, resolveFn, rejectFn := vm.NewPromise()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
	}
	spec, err := parseJobCall(call, e.opts.WorkDir)
	if err == nil && e.opts.Engine == nil {
		err = errors.New("this run cannot run jobs")
	}
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	e.shared.mu.Lock()
	e.shared.execSeq++
	id := e.shared.execSeq
	e.shared.mu.Unlock()
	label := fmt.Sprintf("job-%d", id)
	key := jobKey(spec)
	line := commandLine(spec.Command)
	e.emit(runstore.Event{T: "exec_start", ID: id, Label: label, Command: textutil.ClipOneLine(line, 400)})
	if !e.beginWorker() {
		err := e.ctx.Err()
		if err == nil {
			err = errors.New("workflow is already finishing")
		}
		e.emit(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
		reject(err)
		return vm.ToValue(promise)
	}
	var prior jobRecord
	var hasPrior bool
	if e.opts.Cache != nil {
		prior, hasPrior = e.opts.Cache.popJob(key)
	}
	go func() {
		defer e.workers.Done()
		journal := func(state, jobID string, result any, errStr string) {
			if e.opts.Run == nil {
				return
			}
			e.opts.Run.Journal(runstore.JournalEntry{
				ID: id, Label: label, Key: key, Cwd: spec.Repo, Prompt: line,
				Result: result, Error: errStr, Workflow: e.workflowID,
				Kind: "job", State: state, Job: jobID,
			})
		}
		fail := func(jobID string, err error) {
			journal("error", jobID, nil, err.Error())
			e.emit(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
			reject(err)
		}
		if hasPrior && prior.Done {
			e.emit(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "ok", Cached: true, Preview: cachedSummary(prior.Result)})
			journal("done", prior.Job, prior.Result, "")
			resolve(prior.Result)
			return
		}

		jobID := prior.Job
		if !hasPrior {
			var submitted jobs.Job
			// Do not retry an uncertain submission. Once an ID is known, all
			// later waits and resumes use that ID instead.
			if err := e.opts.Engine.Call(e.ctx, "RunJob", []any{spec}, &submitted); err != nil {
				fail("", fmt.Errorf("submit job: %w", err))
				return
			}
			jobID = submitted.ID
			if jobID == "" {
				fail("", errors.New("submit job: the engine returned no job ID"))
				return
			}
			journal("running", jobID, nil, "")
		}

		var final jobs.Job
		for {
			var waited jobs.WaitResult
			err := retryEngine(e.ctx, recoveryBudget, func() error {
				return e.opts.Engine.Call(e.ctx, "WaitJob", []any{jobID}, &waited)
			})
			if err != nil {
				if e.ctx.Err() != nil {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(e.ctx), jobCancelWait)
					defer cancel()
					if cancelErr := retryEngine(ctx, 0, func() error {
						return e.opts.Engine.Call(ctx, "CancelJob", []any{jobID}, nil)
					}); cancelErr != nil {
						e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("could not cancel job %s: %v", jobID, cancelErr))
					}
				}
				fail(jobID, err)
				return
			}
			if waited.Done {
				final = waited.Job
				break
			}
		}
		result := resultForJob(final)
		journal("done", jobID, result, "")
		e.emit(runstore.Event{
			T: "exec_end", ID: id, Label: label, Status: "ok",
			ExitCode: final.ExitCode, Preview: string(final.State),
		})
		resolve(result)
	}()
	return vm.ToValue(promise)
}
