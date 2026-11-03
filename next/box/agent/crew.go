package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// stepTimeout is how long a worker or command may run when the script sets no timeout, as in v1.
	stepTimeout = 5 * time.Hour
	// schemaAttempts is how many replies a worker gets to match its schema.
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

//go:embed crew_host.js
var crewHostSource string

// crewEngine is what a crew needs from the box socket.
type crewEngine interface {
	WorkflowPin(context.Context, string) error
	WorkflowStepStart(context.Context, contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, error)
	WorkflowStepEnd(context.Context, contract.WorkflowStepEndRequest) error
	WorkflowStepTranscript(context.Context, contract.WorkflowStepTranscriptRequest) error
	WorkflowSetStatus(context.Context, contract.WorkflowSetStatusRequest) error
	WorkflowPost(context.Context, contract.PostRequest) (contract.PostResponse, error)
}

// crewWorker is one running Pi. harness/pi's Harness is one.
type crewWorker interface {
	Start(context.Context, contract.TurnID, []contract.TurnInput, func()) (contract.TurnEnded, error)
	SessionFile() string
	Close() error
}

// CrewConfig is what a crew's box hands it.
type CrewConfig struct {
	Workflow contract.BoxWorkflow
	Profiles []contract.ProfileSummary
	Engine   crewEngine
	// Open starts a worker for a step in dir, its working tree. Folder is the worker's own, for the time it runs, and holds
	// its model configuration in models.json.
	Open       func(ctx context.Context, profile contract.WorkflowProfile, folder, dir string) (crewWorker, error)
	Transcript *Transcript
	Workdir    string
	Branch     string // the session's branch, which names the crew's other branches
	Base       string // the run's pinned commit, when an earlier box reported it
	Sessions   string // Pi's session folder, which all workers share: each gets its own file there
	State      string // the agent's own folder, for the host script and the workers' configuration
}

// Crew runs a workflow in place of a session's Pi: to the runner it is a harness whose one turn is the script. Workers are Pi
// processes in this box, so the session, its box and its socket stay what they are for any other session. The engine's
// journal, not this process, holds what has finished. A restarted box runs the script again and stored steps return at once.
// A reload runs a new version in the same turn, and a step still running here is joined, not started again.
type Crew struct {
	config  CrewConfig
	host    string
	slots   chan struct{}
	builds  *buildLock
	shims   string
	repo    sync.Mutex    // the changes to the one repository that all trees share
	reloads chan struct{} // a newer version of the script is waiting

	mu       sync.Mutex
	workflow contract.BoxWorkflow // the newest version of the script, which a reload replaces
	limits   map[contract.ProfileName]chan struct{}
	running  map[string]*runningStep // by key, the steps this box is running
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

// execution is one run of one version of the script in its own host.
type execution struct {
	version int
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

// NewCrew writes the script host where node can read it.
func NewCrew(config CrewConfig) (*Crew, error) {
	host := filepath.Join(config.State, "crew-host.js")
	if err := os.WriteFile(host, []byte(crewHostSource), 0600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(config.State, "workers"), 0700); err != nil {
		return nil, err
	}
	shims := filepath.Join(config.State, "build-lock")
	builds, err := newBuildLock(shims, buildLockHold)
	if err != nil {
		return nil, err
	}
	c := &Crew{config: config, host: host, slots: make(chan struct{}, max(config.Workflow.Workers, 1)), builds: builds, shims: shims, reloads: make(chan struct{}, 1),
		workflow: config.Workflow, limits: make(map[contract.ProfileName]chan struct{}), running: make(map[string]*runningStep), taken: make(map[contract.DeliverySeq]bool)}
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

func (c *Crew) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.taken[id], nil
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

func (c *Crew) TransferModel(context.Context, contract.TurnID, contract.ModelChoice, int) (harness.ModelTransfer, error) {
	return harness.ModelTransfer{Path: "unchanged"}, nil
}

// Compact does nothing: a crew has no conversation of its own, and a worker's ends with its step.
func (c *Crew) Compact(context.Context, int64) (harness.Compaction, error) {
	return harness.Compaction{}, nil
}

func (c *Crew) SetPromptSections(context.Context, []contract.ContextSection) error { return nil }

// A script takes no input once it runs. The turn's final reply still answers whoever steered it.
func (c *Crew) Steer(context.Context, contract.TurnInput) error    { return nil }
func (c *Crew) FollowUp(context.Context, contract.TurnInput) error { return nil }

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
		result.Final = final
	}
	return result, nil
}

// crewCall is one line from the script host. Payload and Opts identify a step, with Parent, the enclosing workflow's key. Lane
// comes out of the script's options before they reach here, so tagging a step with its subject keeps its key.
type crewCall struct {
	ID      int64           `json:"id"`
	Op      string          `json:"op"`
	Parent  string          `json:"parent"`
	Phase   string          `json:"phase"`
	Lane    string          `json:"lane"`
	Payload json.RawMessage `json:"payload"`
	Opts    json.RawMessage `json:"opts"`
	Key     string          `json:"key"`    // workflow_end
	Value   json.RawMessage `json:"value"`  // end, workflow_end
	Reason  string          `json:"reason"` // end, workflow_end
	run     *execution      // the execution that made the call
}

type crewReply struct {
	ID     int64  `json:"id"`
	Value  any    `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
	Step   string `json:"step,omitempty"`
	stored bool   // the journal had it, so this box may never have run it
}

// script runs the newest version of the script and serves its calls until it reports an end, which is the only way it
// succeeds. A reload stops that version's host and runs the next one. Steps keep running across it, and stop with the script.
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

// execute runs one version in its own host and serves its calls, until the script reports an end or a newer version arrives.
// A step this version started replies to its host, so a stopped host's replies go nowhere.
func (c *Crew) execute(ctx, work context.Context, steps *sync.WaitGroup, workflow contract.BoxWorkflow) (string, bool, error) {
	calls, hostOut, err := os.Pipe()
	if err != nil {
		return "", false, err
	}
	defer calls.Close()
	hostIn, replies, err := os.Pipe()
	if err != nil {
		hostOut.Close()
		return "", false, err
	}
	defer replies.Close()
	var problems tail
	host := exec.CommandContext(ctx, "node", c.host)
	host.Dir, host.Stderr, host.ExtraFiles = c.config.Workdir, &problems, []*os.File{hostOut, hostIn}
	err = host.Start()
	hostOut.Close()
	hostIn.Close()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = host.Process.Kill(); _ = host.Wait() }()
	var writing sync.Mutex
	reply := json.NewEncoder(replies)
	send := func(value any) {
		writing.Lock()
		defer writing.Unlock()
		_ = reply.Encode(value)
	}
	args := workflow.Args
	if args == "" {
		args = "null"
	}
	send(map[string]string{"script": workflow.Script, "args": args})
	run := &execution{version: workflow.Version, seen: make(map[string]int)}
	read, stopped := make(chan crewCall), make(chan struct{})
	defer close(stopped)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		decoder := json.NewDecoder(calls)
		for {
			var call crewCall
			if decoder.Decode(&call) != nil {
				return
			}
			select {
			case read <- call:
			case <-stopped:
				return
			}
		}
	}()
	for {
		var call crewCall
		select {
		case <-c.reloads:
			c.mu.Lock()
			newer := c.workflow.Version != workflow.Version
			c.mu.Unlock()
			if newer {
				return "", true, nil
			}
			continue
		case <-ended:
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			return "", false, fmt.Errorf("the script host stopped: %s", strings.TrimSpace(problems.String()))
		case call = <-read:
		}
		call.run = run
		switch call.Op {
		case "end":
			if call.Reason != "" {
				return "", false, errors.New(call.Reason)
			}
			return finalText(call.Value), false, nil
		case "log", "failed":
			// Inline, so a script's last lines reach the journal before its end does.
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
		default:
			steps.Go(func() {
				out := c.handle(work, call)
				out.ID = call.ID
				send(out)
			})
		}
	}
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
	case "workflow_end":
		end := contract.WorkflowStepEndRequest{Key: call.Key, State: contract.WorkflowStepDone, Result: string(call.Value), Reason: call.Reason}
		if call.Reason != "" {
			end.State = contract.WorkflowStepFailed
		}
		if err := c.config.Engine.WorkflowStepEnd(ctx, end); err != nil {
			return crewReply{Error: err.Error()}
		}
		return crewReply{}
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
	stored, err := c.config.Engine.WorkflowStepStart(ctx, contract.WorkflowStepStartRequest{Key: key, Parent: call.Parent, Kind: info.kind, Name: name, Phase: info.phase, Lane: lane, Profile: info.profile, Detail: detail, Version: call.run.number()})
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
		prompt += "\n\nEnd with only one JSON value that matches this JSON Schema, with no other text:\n" + string(opts.Schema)
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
	limit := stepTimeout
	if opts.Timeout > 0 {
		limit = time.Duration(opts.Timeout) * time.Second
	}
	ctx, cancel := c.builds.timeout(ctx, limit)
	defer cancel()
	folder := filepath.Join(c.config.State, "workers", key)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	defer os.RemoveAll(folder)
	data, err := json.Marshal(WorkerModels(key[:16], c.config.Profiles...))
	if err != nil {
		return stepOutcome{reason: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(folder, "models.json"), data, 0600); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	worker, err := c.config.Open(ctx, profile, folder, dir)
	if err != nil {
		return stepOutcome{reason: "the worker did not start: " + err.Error()}
	}
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
	input := contract.TurnInput{Delivery: delivery, Text: prompt}
	for attempt := 1; ; attempt++ {
		ended, err := worker.Start(ctx, turn, []contract.TurnInput{input}, func() {})
		out.usage.Model = ended.Usage.Model
		out.usage.InputTokens += ended.Usage.InputTokens
		out.usage.OutputTokens += ended.Usage.OutputTokens
		out.usage.CacheReadTokens += ended.Usage.CacheReadTokens
		out.usage.CacheWriteTokens += ended.Usage.CacheWriteTokens
		switch {
		case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
			out.reason = fmt.Sprintf("timed out after %s", limit)
			return out
		case err != nil:
			out.reason = err.Error()
			return out
		case ended.Outcome != contract.TurnCompleted:
			out.reason = ended.Error
			if out.reason == "" {
				out.reason = "the worker's turn " + string(ended.Outcome)
			}
			return out
		case schema == nil && ended.Final == "":
			out.reason = "the worker ended with no text"
			return out
		case schema == nil:
			out.value = ended.Final
			return out
		}
		value, problem := matchSchema(schema, ended.Final)
		if problem == "" {
			out.value = value
			return out
		}
		if attempt == schemaAttempts {
			out.reason = fmt.Sprintf("no reply matched the schema in %d attempts: %s", attempt, problem)
			return out
		}
		input.Text = "That reply does not match the schema: " + problem + "\nReply again with only the JSON value."
	}
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

// matchSchema reads a worker's reply as JSON. Models often fence it, which costs nothing to forgive.
func matchSchema(schema *jsonschema.Schema, reply string) (any, string) {
	text := strings.TrimSpace(reply)
	if strings.HasPrefix(text, "```") {
		_, text, _ = strings.Cut(text, "\n")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return nil, "it is not JSON: " + err.Error()
	}
	if err := schema.Validate(value); err != nil {
		return nil, err.Error()
	}
	return value, ""
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
	info := stepInfo{kind: contract.WorkflowStepCommand, label: opts.Label, phase: opts.Phase, detail: detail}
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

func (c *Crew) command(ctx context.Context, argv []string, opts commandOpts) stepOutcome {
	limit := stepTimeout
	if opts.Timeout > 0 {
		limit = time.Duration(opts.Timeout) * time.Second
	}
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
	err := cmd.Run()
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
	if len(body.Data) > 0 {
		text += "\n\n```json\n" + string(body.Data) + "\n```"
	}
	return c.step(ctx, call, stepInfo{kind: contract.WorkflowStepPost, detail: text}, func(ctx context.Context, key string) stepOutcome {
		if refused != nil {
			return stepOutcome{reason: "post: " + refused.Error()}
		}
		sent, err := c.config.Engine.WorkflowPost(ctx, contract.PostRequest{Text: text, Mentions: mentions, ClientID: key})
		if err != nil {
			return stepOutcome{reason: err.Error()}
		}
		return stepOutcome{value: sent}
	})
}

// workflowCall hands the host a child script from the checkout and its key. It always runs again, so an edited child takes
// effect, and its own steps find their results under this key.
func (c *Crew) workflowCall(ctx context.Context, call crewCall) crewReply {
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(call.Payload, &body); err != nil {
		return crewReply{Error: "workflow: " + err.Error()}
	}
	path := body.Ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.config.Workdir, path)
	}
	info := stepInfo{kind: contract.WorkflowStepWorkflow, detail: body.Ref}
	key, name := c.stepKey(call, ""), info.name()
	if _, err := c.config.Engine.WorkflowStepStart(ctx, contract.WorkflowStepStartRequest{Key: key, Parent: call.Parent, Kind: info.kind, Name: name, Phase: call.Phase, Detail: body.Ref, Version: call.run.number()}); err != nil {
		return crewReply{Error: err.Error(), Step: name}
	}
	script, err := os.ReadFile(path)
	if err != nil {
		if end := c.config.Engine.WorkflowStepEnd(ctx, contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepFailed, Reason: err.Error()}); end != nil {
			err = end
		}
		return crewReply{Error: err.Error(), Step: name}
	}
	return crewReply{Value: map[string]string{"key": key, "script": string(script)}}
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
