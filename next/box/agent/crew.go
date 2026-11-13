package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/MichaelKinsy/PiG/codemode"
	"github.com/resident-labs/dyna/box/dynaext"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// stepTimeout is how long a worker or command may run when the script sets no timeout, as in v1.
	stepTimeout = 5 * time.Hour
	// schemaAttempts is how many submits a schema worker gets.
	schemaAttempts = 3
	// stepResultBytes bounds what one step stores. The rest of a long output belongs in a file in the tree.
	stepResultBytes = 1 << 20
	// stepDetailBytes bounds the prompt or command the journal keeps for the run page.
	stepDetailBytes = 64 << 10
	// commandOutputBytes is how much of each output stream run() returns.
	commandOutputBytes = 64 << 10
	stepNameRunes      = 80
	// laneRunes bounds a step's lane, which names a subject rather than describing it.
	laneRunes = 80
)

// crewEngine is what a crew needs from the box socket.
type crewEngine interface {
	WorkflowPin(context.Context, string) error
	WorkflowStepStart(context.Context, contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, error)
	WorkflowStepEnd(context.Context, contract.WorkflowStepEndRequest) error
	WorkflowStepTranscript(context.Context, contract.WorkflowStepTranscriptRequest) error
	WorkflowSetStatus(context.Context, contract.WorkflowSetStatusRequest) error
	WorkflowState(context.Context, contract.WorkflowStateRequest) (contract.WorkflowStateResponse, error)
	WorkflowPost(context.Context, contract.PostRequest) (contract.PostResponse, error)
}

// crewWorker is one running PiG session. harness/pig's Harness is one.
type crewWorker interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput, func()) (contract.TurnEnded, error)
	Steer(context.Context, contract.TurnInput) error
	Delivered(context.Context, contract.DeliverySeq) (bool, error)
	SessionFile() string
	Close() error
}

// CrewConfig is what a crew's box hands it.
type CrewConfig struct {
	Workflow       contract.BoxWorkflow
	TextOnlyModels []contract.TextOnlyModel
	Engine         crewEngine
	// Open starts a worker for a step in dir, its working tree. Folder is the worker's own, for the time it runs, and holds
	// its model configuration in models.json.
	Open       func(ctx context.Context, profile contract.WorkflowProfile, folder, dir string, submit dynaext.Submit) (crewWorker, error)
	Transcript *Transcript
	Workdir    string
	Branch     string // the session's branch, which names the crew's other branches
	Base       string // the run's pinned commit, when an earlier box reported it
	Sessions   string // PiG's session folder, which all workers share: each gets its own file there
	State      string // the agent's own folder, for the workers' configuration
}

// Crew runs a workflow in place of a session's PiG: to the runner it is a harness whose one turn is the script. Workers are
// PiG sessions in this process, so the session, its box and its socket stay what they are for any other session. The engine's
// journal, not this process, holds what has finished. A restarted box runs the script again and stored steps return at once.
// A reload runs a new version in the same turn, and a step still running here is joined, not started again.
type Crew struct {
	config       CrewConfig
	slots        chan struct{}
	builds       *buildLock
	shims        string
	repo         sync.Mutex    // the changes to the one repository that all trees share
	reloads      chan struct{} // a newer version of the script is waiting
	pauseChanged chan struct{} // a pause change wakes calls waiting to start

	mu       sync.Mutex
	workflow contract.BoxWorkflow // the newest version of the script, which a reload replaces
	limits   map[contract.ProfileName]chan struct{}
	running  map[string]*runningStep               // by key, the steps this box is running
	workers  map[string]crewWorker                 // by key, the workers that are open: a post to the run reaches each
	queued   map[contract.DeliverySeq][]crewWorker // the open workers that queued a post and have not said whether they read it
	taken    map[contract.DeliverySeq]bool
	delivery contract.DeliverySeq // workers carry it, so the engine files their transcripts under the crew's turn
	turn     contract.TurnID
	stop     context.CancelFunc
}

// runningStep is one step in this box. A call with its key waits for its reply.
type runningStep struct {
	done  chan struct{}
	reply crewReply
}

// execution is one run of one version of the script, in its own VM and the VMs of its children.
type execution struct {
	version int
	ctx     context.Context // ends with the version: a reload, or the end of the run
	steps   *sync.WaitGroup // the steps its calls started
	mu      sync.Mutex
	seen    map[string]int // calls to each step hash
}

// number is the version of the script that made a call, or 0 for a call made outside a script.
func (e *execution) number() int {
	if e == nil {
		return 0
	}
	return e.version
}

var _ harness.Harness = (*Crew)(nil)

func NewCrew(config CrewConfig) (*Crew, error) {
	if err := os.MkdirAll(filepath.Join(config.State, "workers"), 0700); err != nil {
		return nil, err
	}
	shims := filepath.Join(config.State, "build-lock")
	builds, err := newBuildLock(shims, buildLockHold)
	if err != nil {
		return nil, err
	}
	c := &Crew{config: config, slots: make(chan struct{}, max(config.Workflow.Workers, 1)), builds: builds, shims: shims, reloads: make(chan struct{}, 1), pauseChanged: make(chan struct{}),
		workflow: config.Workflow, limits: make(map[contract.ProfileName]chan struct{}), running: make(map[string]*runningStep), workers: make(map[string]crewWorker), queued: make(map[contract.DeliverySeq][]crewWorker), taken: make(map[contract.DeliverySeq]bool)}
	c.addLimits(config.Workflow.Profiles)
	return c, nil
}

// addLimits gives each profile with a limit its own slots. A profile that has them keeps them, with the steps that hold them.
func (c *Crew) addLimits(profiles []contract.WorkflowProfile) {
	for _, profile := range profiles {
		if _, ok := c.limits[profile.Name]; !ok && profile.MaxConcurrent > 0 {
			c.limits[profile.Name] = make(chan struct{}, profile.MaxConcurrent)
		}
	}
}

// Reload makes workflow the script this crew runs, when it is newer than the one it has. The running script's host stops and
// the new version starts in its place, in the same turn. Steps already running go on, so a call with one of their keys waits
// for that step, and any the new version does not call stop when the run ends.
func (c *Crew) Reload(workflow contract.BoxWorkflow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if workflow.Paused != c.workflow.Paused {
		c.workflow.Paused = workflow.Paused
		close(c.pauseChanged)
		c.pauseChanged = make(chan struct{})
	}
	if workflow.Version <= c.workflow.Version {
		return
	}
	c.workflow = workflow
	c.addLimits(workflow.Profiles)
	select {
	case c.reloads <- struct{}{}:
	default:
	}
}

// Export sets what every process of this box inherits from now on: the build lock's shims, and one Cargo target folder for all
// trees, because a worktree's own would build every dependency again.
func (c *Crew) Export() error {
	if err := os.Setenv("PATH", c.shims+":"+os.Getenv("PATH")); err != nil {
		return err
	}
	if os.Getenv("CARGO_TARGET_DIR") != "" {
		return nil
	}
	return os.Setenv("CARGO_TARGET_DIR", filepath.Join(c.config.Workdir, "target"))
}

// Done never closes: no Pi outlives a crew's workers, so nothing here ends the box.
func (c *Crew) Done() <-chan struct{} { return nil }

// Files lists every session file in the box, including a killed worker's.
func (c *Crew) Files() []string {
	files, _ := filepath.Glob(filepath.Join(c.config.Sessions, "*.jsonl"))
	return files
}

func (c *Crew) Close() error {
	c.Abort(context.Background())
	_ = c.builds.listener.Close()
	return nil
}

func (c *Crew) Abort(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
	}
	return nil
}

// Delivered reports a turn's own inputs, and a post once a worker's session holds it.
func (c *Crew) Delivered(ctx context.Context, id contract.DeliverySeq) (bool, error) {
	c.mu.Lock()
	taken, workers := c.taken[id], slices.Clone(c.queued[id])
	c.mu.Unlock()
	for _, worker := range workers {
		if taken {
			break
		}
		read, err := worker.Delivered(ctx, id)
		if err != nil {
			return false, err
		}
		taken = read
	}
	return taken, nil
}

// Recover keeps the delivery of a turn a new box takes over, so the workers it starts are filed under that turn too.
func (c *Crew) Recover(_ contract.TurnID, inputs []contract.TurnInput) (contract.TurnEnded, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, input := range inputs {
		c.delivery = input.Delivery
	}
	return contract.TurnEnded{}, false, nil
}

func (c *Crew) Continue(ctx context.Context, id contract.TurnID, started func()) (contract.TurnEnded, error) {
	return c.Start(ctx, id, nil, started)
}

func (c *Crew) TransferModel(context.Context, contract.TurnID, contract.ModelChoice, int, string) (harness.ModelTransfer, error) {
	return harness.ModelTransfer{Path: "unchanged"}, nil
}

// Compact does nothing: a crew has no conversation of its own, and a worker's ends with its step.
func (c *Crew) Compact(context.Context, int64) (harness.Compaction, error) {
	return harness.Compaction{}, nil
}

func (c *Crew) SetPromptSections(context.Context, []contract.ContextSection) error { return nil }

// Steer gives a post to the run to every worker that is running, as a steer of its own turn: each reads it before its next
// model request. The script is code and reads nothing. With no worker running, no one can take the post, and the engine
// keeps it undelivered. The step's result is stored as any other, so its key and a replay do not change.
//
// A worker that queues the post has not read it yet: it may end first. So the post is delivered only once a worker's
// session holds it, which Delivered and the end of each worker check. Every worker is tried before a failure counts, and
// one that queued it is enough: a retry would give it to that worker twice.
func (c *Crew) Steer(ctx context.Context, input contract.TurnInput) error {
	c.mu.Lock()
	workers := slices.Collect(maps.Values(c.workers))
	if input.WorkflowStep != "" {
		workers = nil
		if worker := c.workers[input.WorkflowStep]; worker != nil {
			workers = append(workers, worker)
		}
	}
	c.mu.Unlock()
	var queued []crewWorker
	var failed error
	for _, worker := range workers {
		switch err := worker.Steer(ctx, input); {
		case err == nil:
			queued = append(queued, worker)
		case !errors.Is(err, harness.ErrNoTurn):
			failed = errors.Join(failed, err)
		}
	}
	if len(queued) == 0 {
		if failed != nil {
			return failed
		}
		return harness.ErrNoTurn
	}
	if failed != nil {
		log.Printf("workflow steer %d: %v", input.Delivery, failed)
	}
	c.mu.Lock()
	for _, worker := range queued {
		// The runner may send a post again; a worker is asked about it once.
		if !slices.Contains(c.queued[input.Delivery], worker) {
			c.queued[input.Delivery] = append(c.queued[input.Delivery], worker)
		}
	}
	c.mu.Unlock()
	return nil
}

// settle asks a worker whose turn has ended which queued posts its session holds, and forgets the worker.
func (c *Crew) settle(ctx context.Context, worker crewWorker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, workers := range c.queued {
		at := slices.Index(workers, worker)
		if at < 0 {
			continue
		}
		if read, err := worker.Delivered(ctx, id); err == nil && read {
			c.taken[id] = true
		}
		if c.queued[id] = slices.Delete(workers, at, at+1); len(c.queued[id]) == 0 {
			delete(c.queued, id)
		}
	}
}

// FollowUp is a steer here: a worker has one turn, so input for after it has nowhere else to go.
func (c *Crew) FollowUp(ctx context.Context, input contract.TurnInput) error {
	return c.Steer(ctx, input)
}
func (c *Crew) ClearQueue(context.Context) error { return nil }

// Start runs the script as the session's turn and returns when it settles. A script that throws fails the run; a failed
// worker only fails its step, which the script may handle.
func (c *Crew) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	for _, input := range inputs {
		c.taken[input.Delivery] = true
		c.delivery = input.Delivery
	}
	c.turn, c.stop = id, cancel
	profiles := c.workflow.Profiles
	c.mu.Unlock()
	started()
	result := contract.TurnEnded{Turn: id, Outcome: contract.TurnCompleted}
	if len(profiles) > 0 {
		result.Usage.Model = profiles[0].Model.ID
	}
	var final string
	var err error
	if head, noRepo := runGit(ctx, c.config.Workdir, "", "rev-parse", "HEAD"); noRepo == nil {
		err = c.config.Engine.WorkflowPin(ctx, head)
		if c.config.Base == "" {
			c.config.Base = head
		}
	}
	if err == nil {
		final, err = c.script(ctx)
	}
	switch {
	case ctx.Err() != nil:
		result.Outcome = contract.TurnStopped
	case err != nil:
		result.Outcome, result.Error, result.Cause = contract.TurnFailed, err.Error(), contract.CauseWorkflowFailed
	case len(final) > stepResultBytes:
		result.Outcome, result.Error, result.Cause = contract.TurnFailed, fmt.Sprintf("the result is %d bytes, over the limit of %d", len(final), stepResultBytes), contract.CauseWorkflowFailed
	default:
		var text string
		if json.Unmarshal([]byte(final), &text) == nil && final != "null" {
			result.Final = text
		} else {
			result.Final, result.Data = final, contract.JSONData(final)
		}
	}
	return result, nil
}

// crewCall is one call from a script. Payload and Opts identify a step, with Parent, the enclosing workflow's key. Lane
// comes out of the script's options before they reach here, so tagging a step with its subject keeps its key.
type crewCall struct {
	Op      string          `json:"op"`
	Parent  string          `json:"parent"`
	Phase   string          `json:"phase"`
	Lane    string          `json:"lane"`
	Payload json.RawMessage `json:"payload"`
	Opts    json.RawMessage `json:"opts"`
	run     *execution      // the execution that made the call
}

type crewReply struct {
	Value  any    `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
	Step   string `json:"step,omitempty"`
	stored bool   // the journal had it, so this box may never have run it
}

// script runs the newest version of the script and serves its calls until it ends. A reload stops that version's VM and runs
// the next one. Steps keep running across it, and stop with the script.
func (c *Crew) script(ctx context.Context) (string, error) {
	work, cancel := context.WithCancel(ctx)
	var steps sync.WaitGroup
	defer steps.Wait()
	defer cancel()
	for first := true; ; first = false {
		c.mu.Lock()
		workflow := c.workflow
		c.mu.Unlock()
		if !first {
			log.Printf("workflow %s: running version %d", workflow.Run, workflow.Version)
		}
		final, reloaded, err := c.execute(ctx, work, &steps, workflow)
		if !reloaded {
			return final, err
		}
	}
}

// execute runs one version in a VM of its own and serves its calls, until the script ends or a newer version arrives. A step
// this version started replies to its VM, so a stopped VM's replies go nowhere.
func (c *Crew) execute(ctx, work context.Context, steps *sync.WaitGroup, workflow contract.BoxWorkflow) (string, bool, error) {
	vm, stop := context.WithCancel(ctx)
	run := &execution{version: workflow.Version, seen: make(map[string]int), ctx: vm, steps: steps}
	var reloaded atomic.Bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-c.reloads:
				c.mu.Lock()
				newer := c.workflow.Version != workflow.Version
				c.mu.Unlock()
				if newer {
					reloaded.Store(true)
					stop()
					return
				}
			case <-vm.Done():
				return
			}
		}
	}()
	defer func() { stop(); <-watched }()
	args := workflow.Args
	if args == "" {
		args = "null"
	}
	end, err := c.runScript(work, run, workflow.Script, args, "", 0)
	switch {
	case reloaded.Load():
		return "", true, nil
	case ctx.Err() != nil:
		return "", false, ctx.Err()
	case err != nil:
		return "", false, err
	case end.Failure != nil:
		return "", false, errors.New(end.Failure.Report)
	}
	return end.Value, false, nil
}

// scriptEnd is how a script ended: with its result as JSON text, or with a failure.
type scriptEnd struct {
	Value   string         `json:"value"`
	Failure *scriptFailure `json:"failure"`
}

// scriptFailure is why a script threw. A parent script reads the Reason, a failed run shows the Report, and Step names the
// step whose failure it was, if one did.
type scriptFailure struct {
	Reason string `json:"reason"`
	Report string `json:"report"`
	Step   string `json:"step"`
}

const (
	// crewScriptMemory bounds one script's VM. A script holds the results of its steps, which are at most a megabyte each.
	crewScriptMemory = 256 << 20
	// crewAPI is the names of the script API, which crew_prelude.js builds and the script's own scope declares.
	crewAPI = "state, args, phase, log, console, status, agent, run, merge, post, parallel, pipeline, workflow, Date, Math"
)

//go:embed crew_prelude.js
var crewPrelude string

// metaDeclaration is the start of a script's `export const meta`, which the engine reads and the script only needs as a value.
var metaDeclaration = regexp.MustCompile(`^((?:\s|//[^\n]*\n|/\*[\s\S]*?\*/)*)export\s+const\s+meta\b`)

// runScript runs source in a VM of its own on PiG's codemode sandbox and serves the calls it makes, with args as the script's
// `args`. A child script, at depth 1, is one level down from the run's own, under the key of the step that started it.
// It returns an error only when the VM could not finish: the run stopped, or the sandbox failed.
func (c *Crew) runScript(work context.Context, run *execution, source, args, parent string, depth int) (scriptEnd, error) {
	// A script waits hours for its workers, so the VM has no deadline: every step has its own timeout, and the run's turn
	// ends it when it is stopped.
	sandbox, err := codemode.NewSandbox(codemode.SandboxOptions{TimeoutMs: math.Inf(1), MemoryLimitBytes: crewScriptMemory, Globals: []codemode.Tool{
		{Name: "dyna.call", Execute: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var call crewCall
			if err := json.Unmarshal(raw, &call); err != nil {
				return nil, err
			}
			call.run = run
			replied := make(chan crewReply, 1)
			run.steps.Go(func() { replied <- c.handle(work, call) })
			select {
			case reply := <-replied:
				return json.Marshal(reply)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
		// The prelude sends one line at a time, in order, and the run waits for the last.
		{Name: "dyna.send", Execute: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var call crewCall
			if err := json.Unmarshal(raw, &call); err != nil {
				return nil, err
			}
			call.run = run
			switch call.Op {
			case "log", "failed":
				info := stepInfo{kind: contract.WorkflowStepLog, detail: finalText(call.Payload)}
				c.step(work, call, info, func(context.Context, string) stepOutcome {
					if call.Op == "failed" {
						return stepOutcome{reason: info.detail}
					}
					return stepOutcome{}
				})
			case "status":
				// Not a step, so nothing replays it: a resumed script writes its lines again as it reaches them. The run goes on
				// without one that did not land.
				if err := c.config.Engine.WorkflowSetStatus(work, contract.WorkflowSetStatusRequest{Text: finalText(call.Payload)}); err != nil {
					log.Printf("workflow status: %v", err)
				}
			}
			return nil, nil
		}},
	}})
	if err != nil {
		return scriptEnd{}, err
	}
	defer sandbox.Close()
	argsLiteral, _ := json.Marshal(args)
	parentLiteral, _ := json.Marshal(parent)
	// The script is the body of a function that names the API, with its own block inside, so its declarations may reuse those
	// names. It starts on the first line, so the line numbers of its errors are its own.
	code := "async function script({" + crewAPI + "}) {{" + metaDeclaration.ReplaceAllString(source, "${1}const meta") + "\n}}\n" +
		"return await (" + crewPrelude + ")(dyna.call, dyna.send, " + string(argsLiteral) + ", " + string(parentLiteral) + ", " + strconv.Itoa(depth) + ")(script)"
	result, err := sandbox.Execute(run.ctx, code, codemode.ExecuteOptions{})
	if err != nil {
		return scriptEnd{}, err
	}
	if !result.OK {
		if result.Error.Kind != codemode.ErrorScript {
			return scriptEnd{}, errors.New(result.Error.Message)
		}
		// The script did not parse, so it never ran.
		return scriptEnd{Failure: &scriptFailure{Reason: result.Error.Message, Report: result.Error.Name + ": " + result.Error.Message}}, nil
	}
	var end scriptEnd
	return end, json.Unmarshal(result.Value, &end)
}

// finalText is a run's result as a reply reads: a string as it is, anything else as JSON, and no result as nothing.
func finalText(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

func (c *Crew) handle(ctx context.Context, call crewCall) crewReply {
	if call.Op == "state_get" || call.Op == "state_set" {
		req := contract.WorkflowStateRequest{}
		if call.Op == "state_set" {
			req.Value = call.Payload
		}
		response, err := c.config.Engine.WorkflowState(ctx, req)
		if err != nil {
			return crewReply{Error: err.Error()}
		}
		return crewReply{Value: response.Value}
	}
	switch call.Op {
	case "agent":
		return c.agentCall(ctx, call)
	case "run":
		return c.commandCall(ctx, call)
	case "merge":
		return c.mergeCall(ctx, call)
	case "post":
		return c.postCall(ctx, call)
	case "workflow":
		return c.workflowCall(ctx, call)
	}
	return crewReply{Error: "unknown script call " + call.Op}
}

// stepKey hashes what a step is, never when it ran, so parallel steps replay in any order and an edited one misses. Only a
// true duplicate of an earlier call in this execution gets an ordinal.
func (c *Crew) stepKey(call crewCall, profile contract.ProfileName) string {
	canonical := func(raw json.RawMessage) any {
		var value any
		_ = json.Unmarshal(raw, &value)
		return value
	}
	data, _ := json.Marshal([]any{call.Op, canonical(call.Payload), canonical(call.Opts), call.Parent, profile})
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	ordinal := 0
	if run := call.run; run != nil {
		run.mu.Lock()
		ordinal = run.seen[key]
		run.seen[key]++
		run.mu.Unlock()
	}
	if ordinal > 0 {
		sum = sha256.Sum256(fmt.Appendf(nil, "%s#%d", key, ordinal))
		key = hex.EncodeToString(sum[:])
	}
	return key
}

type stepInfo struct {
	kind    contract.WorkflowStepKind
	feature contract.WorkflowFeature // the switch that gates this step, or none
	label   string
	phase   string
	profile contract.ProfileName
	detail  string
}

// name is how the run's end and a failed entry call the step: its label, or the start of its prompt or command.
func (s stepInfo) name() string {
	if s.label != "" {
		return s.label
	}
	line, _, _ := strings.Cut(strings.TrimSpace(s.detail), "\n")
	if runes := []rune(line); len(runes) > stepNameRunes {
		line = string(runes[:stepNameRunes])
	}
	return string(s.kind) + ": " + line
}

// stepOutcome is what a step did. A nonempty reason means it failed.
type stepOutcome struct {
	value      any
	reason     string
	cause      contract.FailureCause // set for the failures that have a type
	transcript string
	usage      contract.Usage
}

// step journals one call: a stored outcome returns at once, a step running in this box is joined, and anything else runs and
// stores its own. A step cut off by the end of the script or of the box stores nothing, so it runs again.
func (c *Crew) step(ctx context.Context, call crewCall, info stepInfo, work func(context.Context, string) stepOutcome) crewReply {
	if info.phase == "" {
		info.phase = call.Phase
	}
	key, name := c.stepKey(call, info.profile), info.name()
	// After a reload, the new version's call waits for the step the old one started, and its worker.
	c.mu.Lock()
	if joined, ok := c.running[key]; ok {
		c.mu.Unlock()
		select {
		case <-joined.done:
			return joined.reply
		case <-ctx.Done():
			return crewReply{Error: "the run stopped", Step: name}
		}
	}
	own := &runningStep{done: make(chan struct{})}
	c.running[key] = own
	c.mu.Unlock()
	own.reply = c.journal(ctx, call, key, name, info, work)
	c.mu.Lock()
	delete(c.running, key)
	c.mu.Unlock()
	close(own.done)
	return own.reply
}

// journal returns a step's stored outcome, or runs the step under the version that called it and stores what it did.
func (c *Crew) journal(ctx context.Context, call crewCall, key, name string, info stepInfo, work func(context.Context, string) stepOutcome) crewReply {
	detail := info.detail
	if len(detail) > stepDetailBytes {
		detail = strings.ToValidUTF8(detail[:stepDetailBytes], "")
	}
	lane := strings.TrimSpace(call.Lane)
	if runes := []rune(lane); len(runes) > laneRunes {
		lane = string(runes[:laneRunes])
	}
	stored, err := c.config.Engine.WorkflowStepStart(ctx, contract.WorkflowStepStartRequest{Key: key, Parent: call.Parent, Kind: info.kind, Name: name, Phase: info.phase, Lane: lane, Profile: info.profile, Detail: detail, Version: call.run.number(), Feature: info.feature})
	if err != nil {
		return crewReply{Error: err.Error(), Step: name}
	}
	switch stored.State {
	case contract.WorkflowStepDone:
		var value any
		_ = json.Unmarshal([]byte(stored.Result), &value)
		return crewReply{Value: value, stored: true}
	case contract.WorkflowStepFailed:
		return crewReply{Error: stored.Reason, Step: name}
	}
	out := work(ctx, key)
	if ctx.Err() != nil {
		return crewReply{Error: "the run stopped", Step: name}
	}
	end := contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepDone, Reason: out.reason, Transcript: out.transcript, Usage: out.usage}
	data, err := json.Marshal(out.value)
	switch {
	case err != nil:
		end.Reason = err.Error()
	case len(data) > stepResultBytes:
		end.Reason = fmt.Sprintf("the result is %d bytes, over the limit of %d", len(data), stepResultBytes)
	}
	if end.Reason != "" {
		end.State = contract.WorkflowStepFailed
	} else {
		end.Result = string(data)
	}
	if err := c.config.Engine.WorkflowStepEnd(ctx, end); err != nil {
		return crewReply{Error: err.Error(), Step: name}
	}
	if end.Reason != "" {
		return crewReply{Error: end.Reason, Step: name}
	}
	return crewReply{Value: out.value}
}

func strictOpts(raw json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

type agentOpts struct {
	Profile  contract.ProfileName `json:"profile"`
	Schema   json.RawMessage      `json:"schema"`
	Label    string               `json:"label"`
	Phase    string               `json:"phase"`
	Timeout  int                  `json:"timeout"` // seconds
	Worktree bool                 `json:"worktree"`
}

func (c *Crew) agentCall(ctx context.Context, call crewCall) crewReply {
	if err := c.waitUnpaused(ctx); err != nil {
		return crewReply{Error: "the run stopped"}
	}
	var opts agentOpts
	refused := strictOpts(call.Opts, &opts)
	var prompt string
	_ = json.Unmarshal(call.Payload, &prompt)
	c.mu.Lock()
	profiles := c.workflow.Profiles
	c.mu.Unlock()
	var profile *contract.WorkflowProfile
	for i := range profiles {
		if listed := &profiles[i]; opts.Profile == "" || listed.Name == opts.Profile {
			profile = listed
			break
		}
	}
	info := stepInfo{kind: contract.WorkflowStepAgent, label: opts.Label, phase: opts.Phase, profile: opts.Profile, detail: prompt}
	if opts.Worktree {
		info.feature = contract.WorkflowFeatureWorktree
	}
	if profile != nil {
		info.profile = profile.Name
	}
	reply := c.step(ctx, call, info, func(ctx context.Context, key string) stepOutcome {
		if refused != nil {
			return stepOutcome{reason: "agent options: " + refused.Error()}
		}
		if profile == nil {
			return stepOutcome{reason: fmt.Sprintf("profile %q is not in this workflow's meta.profiles", opts.Profile)}
		}
		if opts.Worktree {
			return c.treeWorker(ctx, key, info.name(), *profile, prompt, opts)
		}
		return c.worker(ctx, key, *profile, prompt, opts, c.config.Workdir)
	})
	if opts.Worktree {
		return c.restore(ctx, call, reply)
	}
	return reply
}

func acquire(ctx context.Context, slots chan struct{}) (func(), error) {
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// worker runs one Pi to its final text, or to JSON that matches the schema.
func (c *Crew) worker(ctx context.Context, key string, profile contract.WorkflowProfile, prompt string, opts agentOpts, dir string) (out stepOutcome) {
	var schema *jsonschema.Schema
	if len(opts.Schema) > 0 {
		var err error
		if schema, err = compileSchema(opts.Schema); err != nil {
			return stepOutcome{reason: "schema: " + err.Error()}
		}
		prompt += "\n\nThis step's result is a JSON file that matches this JSON Schema, which you give to the submit tool:\n" + string(opts.Schema)
	}
	c.mu.Lock()
	profileSlots := c.limits[profile.Name]
	c.mu.Unlock()
	for _, slots := range []chan struct{}{c.slots, profileSlots} {
		release, err := acquire(ctx, slots)
		if err != nil {
			return stepOutcome{reason: err.Error()}
		}
		defer release()
	}
	limit := c.timeout(opts.Timeout)
	ctx, cancel := c.builds.timeout(ctx, limit)
	defer cancel()
	folder := filepath.Join(c.config.State, "workers", key)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	defer os.RemoveAll(folder)
	data, err := json.Marshal(WorkerModels(key[:16], c.config.TextOnlyModels...))
	if err != nil {
		return stepOutcome{reason: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(folder, "models.json"), data, 0600); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	var submitted *submission
	var submit dynaext.Submit
	if schema != nil {
		submitted = &submission{}
		submit = submitted.judge(schema, dir)
	}
	worker, err := c.config.Open(ctx, profile, folder, dir, submit)
	if err != nil {
		return stepOutcome{reason: "the worker did not start: " + err.Error()}
	}
	c.mu.Lock()
	c.workers[key] = worker
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.workers, key)
		c.mu.Unlock()
	}()
	file := worker.SessionFile()
	out.transcript, _ = filepath.Rel(c.config.Sessions, file)
	// Only the run page needs this, so a worker runs on without it.
	if err := c.config.Engine.WorkflowStepTranscript(ctx, contract.WorkflowStepTranscriptRequest{Key: key, Transcript: out.transcript}); err != nil {
		log.Printf("workflow step %s transcript: %v", key[:12], err)
	}
	defer func() {
		_ = worker.Close()
		c.config.Transcript.syncLogged(context.WithoutCancel(ctx), file)
	}()
	c.mu.Lock()
	turn, delivery := c.turn, c.delivery
	c.mu.Unlock()
	ended, err := worker.Start(ctx, turn, []contract.TurnInput{{Delivery: delivery, Text: prompt}}, func() {})
	c.settle(context.WithoutCancel(ctx), worker)
	out.usage = contract.Usage{Model: ended.Usage.Model, InputTokens: ended.Usage.InputTokens, OutputTokens: ended.Usage.OutputTokens, CacheReadTokens: ended.Usage.CacheReadTokens, CacheWriteTokens: ended.Usage.CacheWriteTokens}
	switch {
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		out.reason = fmt.Sprintf("timed out after %s", limit)
	case err != nil:
		out.reason = err.Error()
	case submitted != nil && submitted.accepted:
		out.value = submitted.value
	case ended.Outcome != contract.TurnCompleted:
		out.reason = ended.Error
		if out.reason == "" {
			out.reason = "the worker's turn " + string(ended.Outcome)
		}
	case submitted != nil && submitted.attempts == 0:
		out.cause, out.reason = contract.CauseNoSubmit, "the worker ended without submitting a file"
	case submitted != nil:
		out.cause, out.reason = contract.CauseNoSubmit, fmt.Sprintf("the worker ended without a valid submit, after %d submits: %s", submitted.attempts, submitted.problem)
	case ended.Final == "":
		out.reason = "the worker ended with no text"
	default:
		out.value = ended.Final
	}
	return out
}

// submission is what a schema worker's submits come to. judge runs on the worker's tool calls, one at a time.
type submission struct {
	mu       sync.Mutex
	attempts int
	accepted bool
	value    any
	problem  string // the latest submit's
}

var (
	errSubmitOutside = errors.New("the path is outside the working folder")
	errSubmitNotFile = errors.New("the path is not a file")
)

// judge reads the file a worker names, inside dir, as JSON and checks it against the schema. A valid file is the result and
// ends the turn; so does the last of the schemaAttempts files, which fails the step. A refused path is no file and does not count.
func (s *submission) judge(schema *jsonschema.Schema, dir string) dynaext.Submit {
	return func(path string) (string, bool, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		value, problem, refused := readSubmit(schema, dir, path)
		if refused == nil {
			s.attempts++
		}
		if problem == "" && refused == nil {
			s.value, s.accepted = value, true
			return "Submitted: this is the step's result.", true, nil
		}
		s.problem = problem
		if refused != nil {
			s.problem = refused.Error()
		}
		if s.attempts >= schemaAttempts {
			return fmt.Sprintf("%s\nThat was submit %d of %d, so the step fails.", s.problem, s.attempts, schemaAttempts), true, nil
		}
		return problem, false, refused
	}
}

// readSubmit reads the file at path inside dir. A path it may not read is refused; a file that is not JSON, or that the
// schema rejects, comes back as the problem to tell the worker.
func readSubmit(schema *jsonschema.Schema, dir, path string) (value any, problem string, refused error) {
	name := path
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(dir, path)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, "", errSubmitOutside
		}
		name = rel
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	// The root refuses ".." and a symlink that leaves it.
	file, err := root.Open(name)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", errSubmitNotFile
	}
	if info.Size() > stepResultBytes {
		return nil, fmt.Sprintf("the file is %d bytes, over the limit of %d", info.Size(), stepResultBytes), nil
	}
	value, err = jsonschema.UnmarshalJSON(file)
	if err != nil {
		return nil, "it is not JSON: " + err.Error(), nil
	}
	if err := schema.Validate(value); err != nil {
		return nil, err.Error(), nil
	}
	return value, "", nil
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", document); err != nil {
		return nil, err
	}
	return compiler.Compile("schema.json")
}

type commandOpts struct {
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
	Timeout int               `json:"timeout"` // seconds
	Check   *bool             `json:"check"`
	Label   string            `json:"label"`
	Phase   string            `json:"phase"`
}

type commandResult struct {
	Code       int    `json:"code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"durationMs"`
}

func (c *Crew) commandCall(ctx context.Context, call crewCall) crewReply {
	if err := c.waitUnpaused(ctx); err != nil {
		return crewReply{Error: "the run stopped"}
	}
	var opts commandOpts
	refused := strictOpts(call.Opts, &opts)
	var argv []string
	var script string
	if json.Unmarshal(call.Payload, &script) == nil {
		argv = []string{"sh", "-c", script}
	} else if err := json.Unmarshal(call.Payload, &argv); err != nil {
		argv = nil
	}
	detail := strings.Join(argv, " ")
	if script != "" {
		detail = script
	}
	info := stepInfo{kind: contract.WorkflowStepCommand, feature: contract.WorkflowFeatureRun, label: opts.Label, phase: opts.Phase, detail: detail}
	return c.step(ctx, call, info, func(ctx context.Context, _ string) stepOutcome {
		if refused != nil {
			return stepOutcome{reason: "run options: " + refused.Error()}
		}
		if len(argv) == 0 {
			return stepOutcome{reason: "run takes a string or a list of strings"}
		}
		// Builds must not fan out, whatever the script does around them.
		release, err := c.builds.acquire(ctx, nil)
		if err != nil {
			return stepOutcome{reason: err.Error()}
		}
		defer release()
		return c.command(ctx, argv, opts)
	})
}

// timeout is how long a worker or command may run: what the script asked for in seconds, else the room's default for workflows,
// else stepTimeout when the engine sent none.
func (c *Crew) timeout(seconds int) time.Duration {
	c.mu.Lock()
	inherited := c.workflow.Timeout
	c.mu.Unlock()
	if seconds = cmp.Or(seconds, inherited); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return stepTimeout
}

func (c *Crew) command(ctx context.Context, argv []string, opts commandOpts) stepOutcome {
	limit := c.timeout(opts.Timeout)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.config.Workdir
	if opts.Cwd != "" {
		cmd.Dir = filepath.Join(c.config.Workdir, opts.Cwd)
		if filepath.IsAbs(opts.Cwd) {
			cmd.Dir = opts.Cwd
		}
	}
	cmd.Env = append(os.Environ(), "DYNA_BUILD_LOCK=held") // The caller holds it, so a build in this command must not queue.
	for name, value := range opts.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr tail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	// Low priority, like a command of the shell tool: the box agent keeps its CPU while a build uses the cap.
	err := shell.StartLowPriority(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	result := commandResult{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String(), DurationMS: time.Since(started).Milliseconds()}
	out := stepOutcome{value: result}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		out.reason = fmt.Sprintf("timed out after %s", limit)
		return out
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		out.reason = err.Error()
	} else if err != nil && (opts.Check == nil || *opts.Check) {
		out.reason = fmt.Sprintf("exit code %d: %s", result.Code, strings.TrimSpace(result.Stderr))
	}
	return out
}

func (c *Crew) postCall(ctx context.Context, call crewCall) crewReply {
	var body struct {
		To   json.RawMessage `json:"to"`
		Text string          `json:"text"`
		Data json.RawMessage `json:"data"`
	}
	refused := json.Unmarshal(call.Payload, &body)
	var mentions []string
	var one string
	if refused == nil {
		if json.Unmarshal(body.To, &one) == nil {
			mentions = []string{one}
		} else if json.Unmarshal(body.To, &mentions) != nil {
			refused = errors.New("it takes one mention or a list of them")
		}
	}
	text := body.Text
	return c.step(ctx, call, stepInfo{kind: contract.WorkflowStepPost, feature: contract.WorkflowFeaturePost, detail: text}, func(ctx context.Context, key string) stepOutcome {
		if refused != nil {
			return stepOutcome{reason: "post: " + refused.Error()}
		}
		sent, err := c.config.Engine.WorkflowPost(ctx, contract.PostRequest{Text: text, Data: contract.JSONData(body.Data), Mentions: mentions, ClientID: key})
		if err != nil {
			return stepOutcome{reason: err.Error()}
		}
		return stepOutcome{value: sent}
	})
}

// workflowCall runs a child script from the checkout, in a VM of its own, and returns its result. It always runs again, so an
// edited child takes effect, and its own steps find their results under this key.
func (c *Crew) workflowCall(ctx context.Context, call crewCall) crewReply {
	var body struct {
		Ref  string          `json:"ref"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(call.Payload, &body); err != nil {
		return crewReply{Error: "workflow: " + err.Error()}
	}
	path := body.Ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.config.Workdir, path)
	}
	info := stepInfo{kind: contract.WorkflowStepWorkflow, feature: contract.WorkflowFeatureWorkflow, detail: body.Ref}
	key, name := c.stepKey(call, ""), info.name()
	if _, err := c.config.Engine.WorkflowStepStart(ctx, contract.WorkflowStepStartRequest{Key: key, Parent: call.Parent, Kind: info.kind, Name: name, Phase: call.Phase, Detail: body.Ref, Version: call.run.number(), Feature: info.feature}); err != nil {
		return crewReply{Error: err.Error(), Step: name}
	}
	script, err := os.ReadFile(path)
	if err != nil {
		if end := c.config.Engine.WorkflowStepEnd(ctx, contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepFailed, Reason: err.Error()}); end != nil {
			err = end
		}
		return crewReply{Error: err.Error(), Step: name}
	}
	end, err := c.runScript(ctx, call.run, string(script), string(body.Args), key, 1)
	if err != nil {
		// The VM stopped with the run, or with this version of it, so the step is left open for the next to join.
		return crewReply{Error: err.Error(), Step: name}
	}
	stop := contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepDone, Result: end.Value}
	if end.Failure != nil {
		stop.State, stop.Result, stop.Reason = contract.WorkflowStepFailed, "", end.Failure.Reason
	}
	if err := c.config.Engine.WorkflowStepEnd(ctx, stop); err != nil {
		return crewReply{Error: err.Error(), Step: name}
	}
	if end.Failure != nil {
		if end.Failure.Step == "" {
			end.Failure.Step = body.Ref
		}
		return crewReply{Error: end.Failure.Reason, Step: end.Failure.Step}
	}
	return crewReply{Value: json.RawMessage(end.Value)}
}

// tail keeps the last bytes of a stream.
type tail struct {
	mu   sync.Mutex
	data []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if extra := len(t.data) - commandOutputBytes; extra > 0 {
		t.data = t.data[extra:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.data), "")
}

// waitUnpaused gates new agent and command calls; workers already running keep their turns.
func (c *Crew) waitUnpaused(ctx context.Context) error {
	for {
		c.mu.Lock()
		paused, changed := c.workflow.Paused, c.pauseChanged
		c.mu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
