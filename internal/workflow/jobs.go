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

// jobPlanCall is the stable source and branch identity injected by the plan.
type jobPlanCall struct {
	NodeID       string   `json:"__planNodeId"`
	GroupID      string   `json:"__planGroupId"`
	StageIndex   *int     `json:"__stageIndex"`
	ItemIndex    *int     `json:"__itemIndex"`
	Path         []string `json:"__planPath"`
	ReplayUnsafe bool     `json:"__planReplayUnsafe"`
}

func jobPositionHash(workflowID string, plan jobPlanCall) string {
	payload, _ := json.Marshal(struct {
		Workflow string
		Plan     jobPlanCall
	}{workflowID, plan})
	sum := sha256.Sum256(payload)
	return "p1:" + hex.EncodeToString(sum[:])
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

// jobKey keeps request identity independent of position and occurrence.
// Concurrent calls with different commands or options cannot share a key or
// recovered job, even when their plan position is identical.
func jobKey(namespace, requestHash, positionHash string, occurrence int) string {
	payload, _ := json.Marshal(struct {
		Namespace    string
		RequestHash  string
		PositionHash string
		Occurrence   int
	}{namespace, requestHash, positionHash, occurrence})
	sum := sha256.Sum256(payload)
	return "j4:" + hex.EncodeToString(sum[:])
}

// jobRequestHash fingerprints a typed request for its key and recovery check.
func jobRequestHash(spec callspec.RunJobInput) string {
	spec.IdempotencyKey = ""
	spec.RequestHash = ""
	payload, _ := json.Marshal(spec)
	sum := sha256.Sum256(payload)
	return "r1:" + hex.EncodeToString(sum[:])
}

// jobRecord is what a resumed run knows of one earlier job() call.
type jobRecord struct {
	Job         string
	RequestHash string
	Done        bool
	Result      any
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

// job implements one job() call through the engine's job API. Its durable
// idempotency key recovers a submission even when the response or journal was
// lost. Once the journal has an ID, resume waits for that job directly.
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
	var plan jobPlanCall
	if err == nil {
		encoded, marshalErr := json.Marshal(call.Argument(1).Export())
		if marshalErr != nil {
			err = marshalErr
		} else if decodeErr := json.Unmarshal(encoded, &plan); decodeErr != nil {
			err = decodeErr
		}
	}
	if err == nil && e.opts.Engine == nil {
		err = errors.New("this run cannot run jobs")
	}
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	if e.opts.RecordManifest {
		if err := runstore.MarkWorkflowManifestUnsafe(e.ctx, e.runID(), "job calls are not in the workflow manifest"); err != nil {
			e.manifestWriteFailed()
			reject(fmt.Errorf("mark workflow manifest unsafe: %w", err))
			return vm.ToValue(promise)
		}
	}
	requestHash := jobRequestHash(spec)
	positionHash := jobPositionHash(e.workflowID, plan)
	identity := requestHash + "\n" + positionHash
	replayUnsafe := plan.ReplayUnsafe
	e.shared.mu.Lock()
	e.shared.execSeq++
	id := e.shared.execSeq
	occurrence := e.shared.jobOccurrences[identity]
	e.shared.jobOccurrences[identity] = occurrence + 1
	e.shared.mu.Unlock()
	label := fmt.Sprintf("job-%d", id)
	key := jobKey(e.shared.jobNamespace, requestHash, positionHash, occurrence)
	spec.IdempotencyKey = key
	spec.RequestHash = requestHash
	line := commandLine(spec.Command)
	journal := func(state, jobID string, result any, errStr string) {
		if e.opts.Run == nil {
			return
		}
		e.opts.Run.Journal(runstore.JournalEntry{
			ID: id, Label: label, Key: key, Cwd: spec.Repo, Prompt: line,
			Result: result, Error: errStr, Workflow: e.workflowID,
			Kind: "job", State: state, Job: jobID, RequestHash: requestHash, PositionHash: positionHash,
			JobReplayUnsafe: replayUnsafe,
		})
	}
	// No submission can precede this record. Its identity lets a resumed run
	// reject duplicate calls that have no stable branch or call position.
	journal("invoked", "", nil, "")
	cp := e.snapshotCallPlan(plan.NodeID)
	cp.groupID = plan.GroupID
	cp.stage = plan.StageIndex
	cp.item = plan.ItemIndex
	emitJob := func(event runstore.Event) {
		cp.apply(&event)
		e.emit(event)
	}
	emitJob(runstore.Event{T: "exec_start", ID: id, Label: label, Command: textutil.ClipOneLine(line, 400)})
	if !e.beginWorker() {
		err := e.ctx.Err()
		if err == nil {
			err = errors.New("workflow is already finishing")
		}
		journal("error", "", nil, err.Error())
		emitJob(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
		reject(err)
		return vm.ToValue(promise)
	}
	var prior jobRecord
	var hasPrior, ambiguousPrior bool
	if e.opts.Cache != nil {
		ambiguousPrior = e.opts.Cache.ambiguousJob(identity)
		if !ambiguousPrior {
			prior, hasPrior = e.opts.Cache.popJob(key)
		}
	}
	go func() {
		defer e.workers.Done()
		fail := func(jobID string, err error) {
			journal("error", jobID, nil, err.Error())
			emitJob(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "error", Error: textutil.ClipOneLine(err.Error(), 2000)})
			reject(err)
		}
		if ambiguousPrior {
			fail("", errors.New("job() replay is ambiguous: identical calls have no stable branch or call identity"))
			return
		}
		if hasPrior && prior.RequestHash != requestHash {
			fail("", errors.New("job() replay is ambiguous: the recovered request hash does not match"))
			return
		}
		if hasPrior && prior.Done {
			emitJob(runstore.Event{T: "exec_end", ID: id, Label: label, Status: "ok", Cached: true, Preview: cachedSummary(prior.Result)})
			journal("done", prior.Job, prior.Result, "")
			resolve(prior.Result)
			return
		}

		jobID := prior.Job
		if !hasPrior {
			var submitted jobs.Job
			// The engine stores the idempotency key with the job, so retrying an
			// uncertain response returns the original job instead of starting another.
			if err := retryEngine(e.ctx, recoveryBudget, func() error {
				return e.opts.Engine.Call(e.ctx, "RunJob", []any{spec}, &submitted)
			}); err != nil {
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
		emitJob(runstore.Event{
			T: "exec_end", ID: id, Label: label, Status: "ok",
			ExitCode: final.ExitCode, Preview: string(final.State),
		})
		e.loop.RunOnLoop(func(vm *goja.Runtime) {
			value := vm.NewObject()
			_ = value.Set("id", result.ID)
			_ = value.Set("node", result.Node)
			_ = value.Set("state", result.State)
			if result.ExitCode == nil {
				_ = value.Set("exitCode", goja.Null())
			} else {
				_ = value.Set("exitCode", *result.ExitCode)
			}
			_ = value.Set("log", result.Log)
			resolveFn(value)
		})
	}()
	return vm.ToValue(promise)
}
