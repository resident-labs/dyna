// Package harness executes a single worker turn on a concrete agent CLI
// (claude-code, grok, pi, a custom argv, or the built-in mock).
package harness

import (
	"context"
	"crypto/rand"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/scratch"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	harnesshealth "dyna-agent/internal/harness/health"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// Result of one worker invocation.
type Result struct {
	// Execution is the exact reference of the last admitted native attempt.
	Execution     callspec.ExecutionRef
	Model         string // effective model used by the final attempt
	Observed      harnesshealth.Observation
	CLIVersion    string
	Output        string
	Duration      time.Duration
	Nudged        bool // recovered after a transient harness failure
	JournalNudges int  // exact-session journal reminders that successfully started
	// SessionID is the exact harness session the turn ran in, when the harness
	// exposes one. Subagent sessions persist it to resume later turns.
	SessionID string
	// Usage sums the token usage the harness reported across this call's
	// invocations (zero when the dialect reports none).
	Usage              workeroutput.Usage
	UsageNormalization workeroutput.UsageNormalization
	UsageBaseline      *workeroutput.UsageBaseline
	// ContextTokens is the prompt size of the turn's last model call, for
	// harnesses that report usage per call. The next turn passes it back in
	// SessionOptions so Dyna can run the compact command when it is due.
	ContextTokens int64
	// CompactError says why Dyna's compact command failed before this turn.
	// The turn still ran on the uncompacted session.
	CompactError string
	// Compaction records the compaction Dyna ran itself before this turn, so
	// the conversation shows it with trigger dyna. Nil when Dyna ran none.
	Compaction *Compaction
	// AccountID is the saved account the turn ran on, for Dyna-managed harnesses.
	AccountID string
	// AccountSwitch is set when the turn hit its account's usage limit and
	// ran again on another account.
	AccountSwitch *AccountSwitch
}

// Compaction is a compaction Dyna asked a harness's compact command to run.
// Before is the context size Dyna last saw, which is what made it due.
type Compaction struct {
	State  string // completed or failed
	Reason string // why it failed
	Before int64
}

const (
	sessionNudgeDelay          = 2 * time.Second
	journalTerminateGrace      = 500 * time.Millisecond
	cancelTerminateGrace       = 250 * time.Millisecond
	journalCompletionLimit     = 90 * time.Second
	sessionNudgePrompt         = "Continue from where you left off. The previous turn was interrupted by a transient harness/API failure. Inspect the current state, do not redo completed work, and finish the original task. Return the final response requested by the original prompt."
	journalNudgePrompt         = "Your dyna work journal has been quiet for twenty minutes. Write a brief progress entry now with `dyna journal` (one or two sentences plus an optional next step), then continue the original task. Do not stop after reporting status, do not redo completed work, and still return the final response requested by the original prompt."
	journalCompletionPrompt    = "Before finishing, write one brief entry to your dyna work journal now with `dyna journal` (one or two sentences plus an optional next step). Then return the same final response you already produced for the original task. Do not redo the task."
	workerSteeringPollInterval = 100 * time.Millisecond
	steeringDeliveryInterval   = 500 * time.Millisecond
	// DefaultMaxToolWait bounds how long a pending steering message or journal
	// reminder waits before it interrupts a worker whose output shows no tool
	// boundaries (steering.maxToolWait).
	DefaultMaxToolWait = 120 * time.Second

	JournalNudgeIdle    = "idle"
	JournalNudgeMissing = "missing-entry"
)

// JournalIdleEnv overrides the journal-idle reminder delay for the process
// that supervises workers (a Go duration such as "45s"). It exists for
// diagnosing and testing reminders; normal runs leave it unset.
const JournalIdleEnv = "DYNA_JOURNAL_IDLE"

// JournalIdleFromEnv returns $DYNA_JOURNAL_IDLE when it is a valid positive
// duration of at least one second, else def.
func JournalIdleFromEnv(def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(JournalIdleEnv)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			return d
		}
	}
	return def
}

// JournalNudge describes a journal reminder or a reminder that could not be
// delivered safely. Reason is JournalNudgeIdle or JournalNudgeMissing.
type JournalNudge struct {
	Delivered bool
	Reason    string
}

// JournalOptions supervises the run-owned progress journal for a live worker.
// A valid agent-authored entry resets the inactivity deadline. If a resumable
// session stays quiet, dyna interrupts that turn and continues the exact same
// session with journalNudgePrompt; it never starts a fresh replacement worker.
type JournalOptions struct {
	Path                 string
	OutputPath           string // append-only capture of raw harness stdout
	NormalizedOutputPath string // append-only schema-v1 normalized output JSONL
	Output               *workeroutput.Declaration
	IdleAfter            time.Duration
	CompletionTimeout    time.Duration // 0 = 90s; negative = no limit (subagent turns)
	// MaxToolWait bounds how long a due journal reminder or pending steering
	// message waits before it interrupts a worker whose output dialect shows no
	// tool boundaries (0 = DefaultMaxToolWait, negative = wait for the turn to
	// end). A worker whose dialect shows tool boundaries is never interrupted
	// mid-tool: the delivery waits for the next completion or the turn end.
	MaxToolWait time.Duration
	// OnDelivery reports every steering/journal delivery that was deferred to
	// a tool boundary or the turn end, or that interrupted a worker whose
	// dialect exposes no tool boundaries.
	OnDelivery func(DeliveryNote)
	OnEntry    func(runstore.AgentJournalEntry)
	OnNudge    func(JournalNudge)
	State      *JournalState
}

// SteeringOptions identifies the steering source for one worker. It is
// activated only after the harness has an exact resumable session ID.
type SteeringOptions struct {
	Paused       func() bool
	BeforeStart  func(context.Context, string) error
	OnStart      func()
	StartGate    func(context.Context, string, func() error) error
	DeliveryGate func(func() error) error
	RunID        string
	AgentID      int
	// Source replaces the run-owned steering queue (RunID/AgentID) with another
	// store, such as a subagent session's.
	Source SteeringSource
	// OnDispatch records an accepted message before its exact-session
	// continuation can start. Returning an error prevents that continuation.
	OnDispatch func(runstore.SteeringMessage) error
	// OnMessage confirms delivery with its captured complete-line output boundary.
	// A nil boundary is unknown; zero is a proved boundary.
	OnMessage func(runstore.SteeringMessage, *int64)
	pollEvery time.Duration
}

// JournalState keeps the inactivity clock across multiple harness turns that
// belong to one logical agent call (notably JSON-schema correction retries).
type JournalState struct {
	mu              sync.Mutex
	lastActivity    time.Time
	reminded        bool
	unavailableNote bool
	hasEntry        bool
	completionTried bool
	offset          int64
	offsetPath      string
	offsetReady     bool
	reported        map[string]uint8
}

func NewJournalState() *JournalState {
	return &JournalState{lastActivity: time.Now()}
}

// MarkHasEntry records that the worker already keeps its journal, so a finishing
// turn does not get the "write one entry before finishing" reminder (a
// long-lived session journals across turns, not per turn).
func (s *JournalState) MarkHasEntry() {
	s.mu.Lock()
	s.hasEntry = true
	s.mu.Unlock()
}

type commandSpec struct {
	argv                  []string
	stdinPrompt           bool
	prompt                string
	parseOutput           func(string) string
	journalReminderReason string
	steering              []runstore.SteeringMessage
	// acpPrompt runs the text as a session/prompt over the session's
	// long-lived ACP agent process instead of spawning a CLI per turn.
	acpPrompt string
	// streamInput keeps stdin open and writes the prompt, steering, and
	// journal reminders as stream-json user messages (claude-stream-json).
	streamInput bool
	// rpc runs the command over the session's long-lived pi --mode rpc
	// process instead of a per-turn CLI: the prompt travels as an RPC
	// command on spec.argv's session.
	rpc bool
	// finished reads one stdout line of a compact command. It reports
	// whether the command is done and, if so, whether it failed. A nil
	// callback means the command reports success through its exit status.
	finished func(line string) (bool, error)
	// after runs only after this command succeeds.
	after *commandSpec
	// checkOutput validates an exit-status command's protocol response.
	checkOutput func(string) error
}

type invocation struct {
	initial   commandSpec
	cleanup   func()
	sessionID func(string) string
	resume    func(string, string) commandSpec
	// resumeReady reports whether the exact session id can be resumed right
	// now, before any process starts. nil means the harness cannot check.
	resumeReady func(id string) error
	// fork starts a new session from an existing session file in the turn's
	// directory. It is nil for harnesses without an exact-session fork.
	fork func(string, string) commandSpec
	// compact runs the harness's compact command on a session, for harnesses
	// whose compaction rule is Trigger. finished reads its stdout.
	compact func(string) commandSpec
}

type attempt struct {
	output              string
	stdout              string
	stderr              string
	runErr              error
	started             bool
	contextDone         bool
	journalInterrupted  bool
	steering            []runstore.SteeringMessage
	steeringInterrupted bool
	steeringErr         error
	// streamNudges counts journal reminders written to a persistent-input
	// worker's stdin (no interruption).
	streamNudges int
	// streamSteered: steering was written to this attempt's stdin, so its
	// output supersedes any earlier answer.
	streamSteered bool
	// noProgress: dyna stopped the worker because it kept emitting empty steps.
	noProgress bool
	// startupStalled: dyna stopped the worker because it produced no harness
	// event at all within the startup limit.
	startupStalled   bool
	allowEmptyOutput bool
}

type workingDirectoryError struct {
	dir   string
	cause error
}

func (e *workingDirectoryError) Error() string {
	return fmt.Sprintf("working directory %q is unavailable: %v", e.dir, e.cause)
}

func (e *workingDirectoryError) Unwrap() error { return e.cause }

// Run sends prompt to the worker described by p and returns its final message.
func Run(ctx context.Context, p profile.Profile, prompt, cwd string) (Result, error) {
	return RunWithRecovery(ctx, p, prompt, cwd, true)
}

// RunWithRecovery is Run with an explicit same-session recovery budget. The
// engine uses allowRecovery=false after a schema call has already nudged once.
func RunWithRecovery(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool) (Result, error) {
	return run(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay)
}

// ProcessRunner supplies one attempt's stdout, stderr and exit error in process.
// It replaces CLI launch for tests and embedding; the normal output decoders
// and same-session recovery loop still handle each attempt.
// The callback must finish before returning and must honor context cancellation.
type ProcessRunner func(context.Context, []string, io.Writer, io.Writer) error

// ExecutionOptions carries per-invocation execution concerns. They are not
// profile settings and are never persisted on a profile.
type ExecutionOptions struct {
	// SessionKey identifies a caller that has no Dyna session or workflow
	// identity. Reuse it across turns to share one native process.
	SessionKey string

	// Execution names the parent whose native attempts Outbox must admit.
	// Both fields are unset for explicit engine-local embedding.
	Execution callspec.ExecutionRef
	Outbox    *engineclient.Outbox

	// AfterAccountSwitch is set on the rerun after a usage limit moved the
	// turn to another account.
	AfterAccountSwitch bool

	// ProcessRunner replaces process launch and skips CLI version/health probes.
	// Nil uses the normal child process and live process supervision.
	ProcessRunner ProcessRunner

	// AcceptEmptyOutput confirms that this invocation owes no report or already reported its result.
	// It cannot suppress process, protocol, cancellation, or steering errors.
	AcceptEmptyOutput func() bool

	// AcceptCleanEmptyStop finishes a turn the harness ended cleanly with no
	// final text, even when it owes a report: the report then carries no
	// summary. A turn that needs a structured result must not set it. It cannot
	// suppress process, protocol, cancellation, or steering errors.
	AcceptCleanEmptyStop bool

	// ConstrainProfile applies service permissions after private runtime
	// preparation, so generated configuration cannot widen those permissions.
	ConstrainProfile func(profile.Profile) (profile.Profile, error)

	// SkipComputer prevents provisioning a desktop for a managed service turn.
	SkipComputer bool
	// HealthProbe leaves health persistence to the caller after probe validation.
	HealthProbe bool
	// KillOnCancel immediately kills the worker process group at a hard deadline.
	KillOnCancel bool
	// DisableNativeSubagents runs the worker as a leaf: the harness's own
	// native subagent facilities are disabled for this invocation (and its
	// exact-session continuations) where the harness supports a control.
	// Workflow agent() workers and subagent session turns always set it.
	DisableNativeSubagents bool
	// Started is told the PID of each harness process right after it starts,
	// so a workflow agent's process can be recorded for its agent number.
	Started func(pid int)
	// DynaShell replaces the harness's native shell with the shell tool of
	// Dyna's MCP server. Only session turns set it: their background jobs
	// wake the session when they end.
	DynaShell bool
	// streamStallLimit overrides the acknowledgment stall bound for tests.
	// Zero keeps the production default. Each invocation owns its limit.
	streamStallLimit time.Duration
	// emptyStepLimit overrides the empty-step bound for tests.
	emptyStepLimit int
	// startupLimit overrides how long a turn may produce no harness event at
	// all before it fails. Zero keeps the production default.
	startupLimit time.Duration
	// startupFired, when set, fires the startup limit instead of its timer,
	// so a test can fire it after the worker reached a known point.
	startupFired <-chan time.Time
}

// RunWithJournal adds live progress-journal supervision to RunWithRecovery.
// It is kept separate so non-workflow callers retain the simple Run API.
func RunWithJournal(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, journal JournalOptions) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay, journal, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
}

// RunWithOptions is the full invocation form: journal, steering, session, and
// execution options in one call. Workflow workers pass
// ExecutionOptions{DisableNativeSubagents: true} here.
func RunWithOptions(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, journal JournalOptions, steering SteeringOptions, session SessionOptions, exec ExecutionOptions) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay, journal, steering, session, exec)
}

// SessionOptions makes one call a turn of a long-lived harness session.
type SessionOptions struct {
	// RecoveryDelay overrides the delay before a same-session recovery retry.
	// Nil keeps the default; tests can use zero to avoid a real wait.
	RecoveryDelay *time.Duration

	RuntimePin *harnessruntime.Pin
	OnRuntime  func(harnessruntime.Pin) error

	// ResumeID continues this exact harness session instead of starting a new
	// one. Resuming a harness that cannot resume is an error, never a silent
	// fresh start.
	ResumeID string
	// ForkFrom forks the given session file into this turn's directory and
	// starts a new session instead of opening it in place. The fork's id is the
	// invocation's new session id, reported through OnSession. It is used when
	// the source session's own directory no longer exists, because Pi would
	// otherwise ask whether to fork on stdin.
	ForkFrom string
	// UsageBaseline must belong to the immediately preceding completed Dyna turn.
	UsageBaseline *workeroutput.UsageBaseline
	// OnSession is called once with the exact harness session id as soon as it
	// is known, so a caller can persist it before the turn ends.
	OnSession func(id string)
	// OnCompaction runs when stdout reports a verified conversation compaction,
	// or when Dyna's compact command succeeded before the turn.
	OnCompaction func()
	// ContextTokens is the previous turn's Result.ContextTokens. A resumed
	// turn first runs the harness's compact command when profile.CompactDue.
	ContextTokens int64
	// OnFallback runs when the turn leaves a model for the next one in the
	// profile's chain, so the caller can record why.
	OnFallback func(ModelFallback)
}

// ModelFallback records one move to the next model in a fallback chain.
type ModelFallback struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// RunSessionTurn runs one turn of a session: it starts a new harness session,
// or resumes opts.ResumeID, with the same journal/steering supervision as a
// workflow worker.
func RunSessionTurn(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, journal JournalOptions, steering SteeringOptions, session SessionOptions) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay, journal, steering, session, ExecutionOptions{DisableNativeSubagents: true})
}

// Resumable reports whether the profile's harness can continue an exact
// session (and so accept follow-up turns and steering).
func Resumable(p profile.Profile) bool {
	if p.Harness == profile.HarnessMock {
		return true
	}
	inv, err := buildInvocation(p, "")
	if inv.cleanup != nil {
		inv.cleanup()
	}
	return err == nil && inv.resume != nil
}

// PrepareRuntime selects the Dyna-managed harness copy, account and config
// generation for one turn. release ends the leases that protect them. Tests
// that launch fake CLIs replace it with fakeruntime.Prepare.
var PrepareRuntime = prepareRuntime

// prepareRoomGitHub gives one turn the room's GitHub account. Tests replace it.
var prepareRoomGitHub = roomGitHubEnvironment

// roomGitHubEnvironment returns the environment the turn needs, with the one
// line the agent gets instead when GitHub is limited.
func roomGitHubEnvironment(ctx context.Context, p profile.Profile) (map[string]string, string, error) {
	binary, err := fsutil.StableExecutable()
	if err != nil {
		return nil, "", err
	}
	inherited := append([]string{}, os.Environ()...)
	for key, value := range p.Env {
		inherited = append(inherited, key+"="+value)
	}
	sessionEnv, err := ghclient.SessionEnvironment(ctx, p.Env[mode.EnvRoomID], p.Env[scratch.Env], binary, inherited)
	if err != nil {
		return nil, "", fmt.Errorf("prepare room GitHub access: %w", err)
	}
	return sessionEnv.Env, sessionEnv.Notice, nil
}

func prepareRuntime(ctx context.Context, p profile.Profile, previous *harnessruntime.Pin) (_ profile.Profile, _ harnessruntime.Pin, _ func(), err error) {
	var closers []io.Closer
	// A local func, so an error return cannot replace it before the defer runs.
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i].Close()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	lease, err := harnessruntime.NewAccounts().TurnLeaseManaged(ctx, p.Harness)
	if err != nil {
		return p, harnessruntime.Pin{}, nil, err
	}
	closers = append(closers, lease)
	selected, pin, generationLease, err := harnessruntime.New().PrepareLeased(ctx, p, previous)
	if err != nil {
		return p, pin, nil, err
	}
	closers = append(closers, generationLease)
	bundleLease, err := harnessruntime.NewBundleManager().LeaseManaged(ctx, pin.Bundle)
	if err != nil {
		return p, pin, nil, err
	}
	closers = append(closers, bundleLease)
	selected.RuntimeBindProcess = func(reference json.RawMessage) error {
		if err := lease.Bind(reference); err != nil {
			return err
		}
		if err := bundleLease.Bind(reference); err != nil {
			return err
		}
		return generationLease.Bind(reference)
	}
	return selected, pin, cleanup, nil
}

func run(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, nudgeDelay, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
}

func runJournaled(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration, journal JournalOptions, steeringOpts SteeringOptions, session SessionOptions, exec ExecutionOptions) (Result, error) {
	return withAccountFailover(ctx, p, cwd, exec, func(e ExecutionOptions, resume string) (Result, error) {
		sess, task := session, prompt
		if resume != "" {
			sess.ResumeID, sess.ForkFrom, task = resume, "", accountFailoverPrompt
		}
		return runJournaledOnce(ctx, p, task, cwd, allowRecovery, nudgeDelay, journal, steeringOpts, sess, e)
	})
}

func runJournaledOnce(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration, journal JournalOptions, steeringOpts SteeringOptions, session SessionOptions, exec ExecutionOptions) (Result, error) {
	if session.RuntimePin != nil {
	}
	if session.RecoveryDelay != nil {
		nudgeDelay = *session.RecoveryDelay
	}
	if session.RuntimePin != nil && session.ResumeID == "" {
		if h, err := harnessruntime.CanonicalHarness(p.Harness); err == nil && h != session.RuntimePin.Bundle.Harness {
			session.RuntimePin = nil
		}
	}
	if session.RuntimePin != nil {
		p.RuntimeVersion = session.RuntimePin.Bundle.Version
	}
	accountID := ""
	// A ProcessRunner replaces the process, so no private copy is needed.
	if p.DynaManaged() && exec.ProcessRunner == nil {
		withRepo, err := harnessruntime.New().SelectAccount(ctx, p, cwd)
		if err != nil {
			return Result{}, notReady(ctx, p, err)
		}
		p = withRepo

		selected, pin, release, err := PrepareRuntime(ctx, p, session.RuntimePin)
		if err != nil {
			return Result{}, notReady(ctx, p, err)
		}
		defer release()
		p, accountID = selected, pin.AccountID
		if session.OnRuntime != nil {
			if err := session.OnRuntime(pin); err != nil {
				return Result{}, notReady(ctx, p, err)
			}
		}
	}
	if p.Env[mode.EnvRoomID] != "" && p.Harness != profile.HarnessMock && exec.ProcessRunner == nil {
		env, notice, err := prepareRoomGitHub(ctx, p)
		if err != nil {
			return Result{}, err
		}
		for key, value := range env {
			p.Env[key] = value
		}
		// GitHub being limited costs the agent one line, never the turn.
		if notice != "" {
			prompt += "\n\n" + notice
		}
	}
	if exec.ConstrainProfile != nil {
		var err error
		p, err = exec.ConstrainProfile(p)
		if err != nil {
			return Result{}, err
		}
	}
	var version harnesshealth.VersionInfo
	if exec.ProcessRunner == nil {
		version = harnesshealth.VersionDetector(ctx, p)
	} else {
		// Recorded attempts do not describe an installed CLI's health.
		exec.HealthProbe = true
	}
	state := &runState{normalizer: workeroutput.NewUsageNormalizer(session.ResumeID, session.UsageBaseline), onCompaction: session.OnCompaction}
	stopProgress := func() {}
	if !exec.HealthProbe {
		stopProgress = startHealthProgress(p, version.Version, state)
	}
	result, err := runJournaledState(ctx, p, prompt, cwd, allowRecovery, nudgeDelay, journal, steeringOpts, session, exec, state, accountID)
	result.AccountID = accountID
	if ctx.Err() == nil && !isNativeAdmissionError(err) {
		err = classifyFailure(err, state.capacityTries)
	}
	if state.tracker != nil {
		result.SessionID, result.Usage = state.snapshot()
	}
	result.UsageNormalization = state.normalizer.Normalization()
	result.UsageBaseline = state.normalizer.Baseline()
	result.ContextTokens, result.CompactError = state.compactValues()
	if err != nil {
		result.UsageNormalization.MarkPartial("invocation did not complete")
		// A failed turn cannot prove what the provider billed, so the saved
		// checkpoint must not survive it: the next turn would subtract it and
		// report the missing spend as its own, complete.
		result.UsageBaseline = nil
	}
	// Stop the progress writer before publishing the completed observation, so
	// a late progress write cannot replace the final health result.
	stopProgress()
	state.mu.Lock()
	observed := state.health
	state.mu.Unlock()
	observed.Successful = err == nil
	observed.PositiveUsageSeen = result.Usage.Total() > 0
	if p.Harness == profile.HarnessMock && result.Output != "" {
		observed.StdoutSeen, observed.TextSeen = true, true
		observed.RecognizedRecords++
	}
	result.Observed = observed
	result.CLIVersion = version.Version
	// Health is best effort. It never changes the final answer or turn error.
	if !exec.HealthProbe {
		harnesshealth.New("").RecordTurn(p, version.Version, observed)
	}
	return result, err
}

// journaledRun carries the mutable state of one runJournaledState call, so
// each step can be a plain method. Recovery and model fallback replace p, inv,
// spec and tracker while the loop runs.
type journaledRun struct {
	ctx           context.Context
	p             profile.Profile
	prompt, cwd   string
	allowRecovery bool
	nudgeDelay    time.Duration
	journal       JournalOptions
	steeringOpts  SteeringOptions
	session       SessionOptions
	exec          ExecutionOptions
	accountID     string
	execution     callspec.ExecutionRef
	state         *runState

	candidates   []string
	candidateIdx int
	start        time.Time

	inv        invocation
	spec       commandSpec
	tracker    *sessionTracker
	supervisor *journalSupervisor
	steering   *steeringSupervisor
	// compaction is the compaction Dyna ran itself before this turn.
	compaction *Compaction
	gate       *deliveryGate

	recoveryErr                 error
	lastOutput                  string
	recovered                   bool
	journalNudges               int
	completionReminderAttempted bool
	completionOutput            string
}

// stepAct tells the attempt loop what to do after a step.
type stepAct int

const (
	actNext     stepAct = iota // this step did not apply; run the next one
	actContinue                // start another attempt
	actReturn                  // return the step's result and error
)

// runJournaledState runs one worker turn: prepare, supervise attempts, recover.
// The deferred result.Model read and the deferred cleanups run in the original
// order: stopSteering, then the invocation cleanup, then result.Model.
func runJournaledState(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration, journal JournalOptions, steeringOpts SteeringOptions, session SessionOptions, exec ExecutionOptions, state *runState, accountID string) (result Result, runErr error) {
	p.NormalizeModels()
	selected, candidates := SelectHealthyModel(p, cooldownNow())
	p.Model = selected
	r := &journaledRun{
		ctx: ctx, p: p, prompt: prompt, cwd: cwd, allowRecovery: allowRecovery, nudgeDelay: nudgeDelay,
		journal: journal, steeringOpts: steeringOpts, session: session, exec: exec, state: state,
		candidates: candidates,
		accountID:  accountID,
	}
	defer func() { result.Model = r.p.Model }()
	r.start = time.Now()
	if r.p.Harness == profile.HarnessMock {
		return r.runMockTurn()
	}

	if err := r.prepareInvocation(); err != nil {
		return Result{}, err
	}
	defer func() {
		if r.inv.cleanup != nil {
			r.inv.cleanup()
		}
	}()
	if err := r.startTracking(); err != nil {
		return Result{}, err
	}
	stopSteering, err := r.startSteering()
	defer stopSteering()
	if err != nil {
		return Result{}, err
	}
	if err := r.compactBeforeTurn(); err != nil {
		return r.result(""), err
	}
	r.openDelivery()
	var out Result
	out, err = r.superviseAttempts()
	// The compaction ran before the turn, so every way the turn ends reports it.
	out.Compaction = r.compaction
	return out, err
}

// runMockTurn runs the mock harness, which has no process to supervise.
func (r *journaledRun) runMockTurn() (Result, error) {
	if text, ok := mockUsageLimit(r.prompt); ok && !r.exec.AfterAccountSwitch {
		return Result{}, providerfailure.Tag(providerfailure.Quota, errors.New(text))
	}
	if r.session.ResumeID != "" || r.session.OnSession != nil || r.steeringOpts.Source != nil {
		return runMockSession(r.ctx, r.prompt, r.journal, r.steeringOpts, r.session, r.state)
	}
	if r.journal.Path != "" {
		e := runstore.AgentJournalEntry{
			TS: time.Now().UnixMilli(), Kind: "update", Message: "Mock worker oriented to the task.",
			Next: "Produce the deterministic mock response.", Source: "agent",
		}
		if err := runstore.AppendAgentJournalPath(r.journal.Path, e); err == nil && r.journal.OnEntry != nil {
			r.journal.OnEntry(e)
		}
	}
	out, err := runMock(r.ctx, r.prompt)
	return Result{Output: out, Duration: time.Since(r.start)}, err
}

// prepareInvocation builds the first command.
func (r *journaledRun) prepareInvocation() error {
	var err error
	r.inv, err = buildInvocationInDirExec(r.p, r.prompt, r.cwd, r.exec)
	return err
}

// startTracking creates the session tracker and presets a resumed session id.
func (r *journaledRun) startTracking() error {
	r.tracker = newSessionTracker(r.inv.sessionID)
	r.state.tracker = r.tracker
	if r.session.ForkFrom != "" {
		if r.session.ResumeID != "" {
			return errors.New("a turn cannot both fork a session and resume another")
		}
		if r.inv.fork == nil {
			return fmt.Errorf("worker %s cannot fork a session: its harness has no exact-session fork", r.p.Name)
		}
	}
	if r.session.ResumeID != "" {
		if r.inv.resume == nil {
			return fmt.Errorf("worker %s cannot resume harness session %s: its harness has no exact-session resume", r.p.Name, r.session.ResumeID)
		}
		r.tracker.preset(r.session.ResumeID)
	}
	r.tracker.onSession = r.session.OnSession
	r.tracker.observe("") // Claude/Pi preassign an id without reading stdout.
	return nil
}

// startSteering creates both supervisors. The caller must defer the returned
// stop function even when err is not nil.
func (r *journaledRun) startSteering() (stop func(), err error) {
	r.supervisor = newJournalSupervisor(r.journal)
	r.steering = newSteeringSupervisor(r.steeringOpts, r.inv.resume != nil)
	steering := r.steering
	if steering == nil {
		return func() {}, nil
	}
	stop = func() {
		steering.close()
	}
	return stop, steering.activateIfReady(r.tracker.idValue())
}

// compactBeforeTurn runs the optional pre-turn compaction and picks the first
// command: a fresh start or a resume of the given session.
func (r *journaledRun) compactBeforeTurn() error {
	if r.session.ForkFrom != "" {
		// A fork starts the target session with the whole source history; Pi
		// compacts it when the first request needs more room.
		r.spec = r.inv.fork(r.session.ForkFrom, r.prompt)
		return nil
	}
	r.spec = r.inv.initial
	if r.session.ResumeID == "" {
		return nil
	}
	if r.inv.resumeReady != nil {
		if err := r.inv.resumeReady(r.session.ResumeID); err != nil {
			// Resuming a session whose file is gone would only push a prompt
			// into a process that exits on the missing session. Fail clearly,
			// so the caller can restart or move the work elsewhere.
			return fmt.Errorf("worker %s cannot resume harness session %s: %w", r.p.Name, r.session.ResumeID, err)
		}
	}
	if r.inv.compact != nil && profile.CompactDue(r.p, r.session.ContextTokens) {
		completed, err := runCompaction(r.ctx, r.p, r.cwd, r.inv.compact(r.session.ResumeID), r.steering, r.exec, r.admitNativeAttempt)
		if isNativeAdmissionError(err) {
			return err
		}
		if err != nil {
			r.state.setCompactError(err)
			r.compaction = &Compaction{State: "failed", Reason: err.Error(), Before: r.session.ContextTokens}
		} else if completed > 0 {
			// The event carries the context size that made the compaction due.
			r.compaction = &Compaction{State: "completed", Before: r.session.ContextTokens}
			if r.session.OnCompaction != nil {
				r.session.OnCompaction()
			}
		}
	}
	r.spec = r.inv.resume(r.session.ResumeID, r.prompt)
	return nil
}

// openDelivery sets the output declaration and the steering delivery gate.
func (r *journaledRun) openDelivery() {
	if r.journal.Output == nil {
		r.journal.Output = r.p.EffectiveOutput()
	}
	r.gate = newDeliveryGate(r.journal)
}

// result builds a Result with the current nudge counts.
func (r *journaledRun) result(out string) Result {
	return Result{Execution: r.execution, Output: out, Duration: time.Since(r.start), Nudged: r.recovered, JournalNudges: r.journalNudges, Compaction: r.compaction}
}

// superviseAttempts runs attempts until one step returns a final result.
func (r *journaledRun) superviseAttempts() (Result, error) {
	for {
		if err := r.admitNativeAttempt(r.ctx); err != nil {
			return r.result(r.lastOutput), err
		}
		a := r.runAttempt()
		act, res, err := r.resumeInterrupted(a)
		if act == actNext {
			if attemptErr := r.attemptFailure(a); attemptErr == nil {
				act, res, err = r.completeAttempt(a)
			} else {
				act, res, err = r.failAttempt(a, attemptErr)
			}
		}
		if act == actReturn {
			return res, err
		}
	}
}

// runAttempt starts the worker process once and records what it did.
func (r *journaledRun) runAttempt() attempt {
	attemptCtx := r.ctx
	cancelAttempt := func() {}
	if r.spec.journalReminderReason == JournalNudgeMissing {
		limit := r.journal.CompletionTimeout
		if limit == 0 {
			limit = journalCompletionLimit
		}
		if limit > 0 {
			attemptCtx, cancelAttempt = context.WithTimeout(r.ctx, limit)
		}
	}
	recordClaudeUsage := captureClaudeUsage(attemptCtx, r.p, r.execution, r.exec.Outbox)
	a := runOnce(attemptCtx, r.p, r.cwd, r.spec, r.tracker, r.supervisor, r.steering, r.inv.resume != nil, r.journal.OutputPath, r.journal.NormalizedOutputPath, r.journal.Output, recordClaudeUsage.Observe, r.gate, r.state, r.exec)
	recordClaudeUsage.Finish()
	cancelAttempt()
	r.journalNudges += a.streamNudges
	if a.streamSteered {
		// The worker answered steering in this attempt (for example during
		// a completion reminder); its output is the turn's answer now.
		r.completionReminderAttempted = false
		r.completionOutput = ""
	}
	if r.spec.journalReminderReason != "" && a.started {
		r.journalNudges++
	} else if r.spec.journalReminderReason != "" && r.supervisor != nil {
		r.supervisor.markNudged(false, r.spec.journalReminderReason)
	}
	r.tracker.observe(a.stdout)
	if a.output != "" {
		r.lastOutput = a.output
	}
	return a
}

// resumeInterrupted continues the session after a steering or journal interrupt.
func (r *journaledRun) resumeInterrupted(a attempt) (stepAct, Result, error) {
	if (a.steeringInterrupted || len(a.steering) > 0) && r.ctx.Err() == nil {
		if err := r.resumeSteering(a.steering); err != nil {
			return actReturn, r.result(r.lastOutput), err
		}
		return actContinue, Result{}, nil
	}
	if a.journalInterrupted && r.ctx.Err() == nil {
		sessionID := r.tracker.idValue()
		if sessionID == "" || r.inv.resume == nil {
			return actReturn, r.result(r.lastOutput),
				fmt.Errorf("worker %s was interrupted for a journal reminder but its exact session could not be resumed", r.p.Name)
		}
		r.spec = r.inv.resume(sessionID, journalNudgePrompt)
		r.spec.journalReminderReason = JournalNudgeIdle
		return actContinue, Result{}, nil
	}
	return actNext, Result{}, nil
}

// attemptFailure classifies the finished attempt; nil means it succeeded.
func (r *journaledRun) attemptFailure(a attempt) error {
	if a.output == "" && r.exec.AcceptEmptyOutput != nil {
		a.allowEmptyOutput = r.exec.AcceptEmptyOutput()
	}
	if a.output == "" && r.exec.AcceptCleanEmptyStop && cleanEmptyStop(r.p, a.stdout) {
		a.allowEmptyOutput = true
	}
	return attemptError(r.ctx, r.p, r.spec.argv, a)
}

// resumeSteering prepares a continuation that delivers steering messages.
func (r *journaledRun) resumeSteering(messages []runstore.SteeringMessage) error {
	if len(messages) == 0 {
		return nil
	}
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("worker %s canceled/timed out before steering continuation: %w", r.p.Name, err)
	}
	sessionID := r.tracker.idValue()
	if sessionID == "" || r.inv.resume == nil {
		return fmt.Errorf("worker %s received steering but its exact session could not be resumed", r.p.Name)
	}
	if err := r.steering.dispatch(messages); err != nil {
		return fmt.Errorf("record steering dispatch for worker %s: %w", r.p.Name, err)
	}
	// Dispatch callbacks may race with external cancellation. CommandContext
	// below is the final pre-Start gate, but avoid preparing a continuation
	// at all when cancellation is already visible here.
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("worker %s canceled/timed out before steering continuation: %w", r.p.Name, err)
	}
	if r.supervisor != nil {
		r.supervisor.noteExternalResume()
	}
	r.completionReminderAttempted = false
	r.completionOutput = ""
	r.spec = r.inv.resume(sessionID, steeringPrompt(messages))
	r.spec.steering = append([]runstore.SteeringMessage(nil), messages...)
	return nil
}

// finishOrSteer polls for late steering; true means a continuation is ready.
func (r *journaledRun) finishOrSteer() (bool, error) {
	if r.steering == nil {
		return false, nil
	}
	messages, err := r.steering.finish()
	if err != nil {
		return false, err
	}
	if err := r.ctx.Err(); err != nil {
		return false, fmt.Errorf("worker %s canceled/timed out after final steering poll: %w", r.p.Name, err)
	}
	if len(messages) == 0 {
		return false, nil
	}
	return true, r.resumeSteering(messages)
}

// finish ends the turn with out and retErr unless late steering continues it.
func (r *journaledRun) finish(out string, retErr error) (stepAct, Result, error) {
	resume, finishErr := r.finishOrSteer()
	if finishErr != nil {
		return actReturn, r.result(out), finishErr
	}
	if resume {
		return actContinue, Result{}, nil
	}
	return actReturn, r.result(out), retErr
}

// completeAttempt handles a successful attempt, with one journal completion
// reminder for a worker that wrote no journal entry.
func (r *journaledRun) completeAttempt(a attempt) (stepAct, Result, error) {
	ClearModelFailure(r.p.Name, r.p.Model)
	// A short worker can finish before the inactivity deadline. Give every
	// resumable worker one exact-session chance to satisfy the journal
	// contract, while preserving its already-successful task result.
	//
	// Read the journal once more first: a worker can write its entry and
	// finish between two polls, so this check, not the last poll, decides.
	if r.supervisor != nil {
		r.supervisor.poll()
	}
	if r.supervisor != nil && !r.supervisor.hasAgentEntry() {
		if r.completionReminderAttempted {
			r.supervisor.markNudged(false, JournalNudgeMissing)
			return r.finish(r.completionOutput, nil)
		}
		if !r.supervisor.claimCompletionReminder() {
			return r.finish(a.output, nil)
		}
		sessionID := r.tracker.idValue()
		if sessionID == "" || r.inv.resume == nil {
			r.supervisor.markNudged(false, JournalNudgeMissing)
			return r.finish(a.output, nil)
		}
		r.completionOutput = a.output
		r.completionReminderAttempted = true
		r.spec = r.inv.resume(sessionID, journalCompletionPrompt)
		r.spec.journalReminderReason = JournalNudgeMissing
		return actContinue, Result{}, nil
	}
	if r.completionReminderAttempted {
		return r.finish(r.completionOutput, nil)
	}
	return r.finish(a.output, nil)
}

// failAttempt handles a failed attempt: reminder failure, model fallback,
// a failed same-session nudge, or a first same-session recovery.
func (r *journaledRun) failAttempt(a attempt, attemptErr error) (stepAct, Result, error) {
	if r.completionReminderAttempted {
		if r.ctx.Err() != nil {
			return actReturn, r.result(r.completionOutput), attemptErr
		}
		// Journaling is a progress side channel. Do not turn an already-valid
		// task result into a failure if its completion reminder cannot finish.
		r.supervisor.markNudged(false, JournalNudgeMissing)
		return r.finish(r.completionOutput, nil)
	}
	if act, res, err := r.fallbackModel(a, attemptErr); act != actNext {
		return act, res, err
	}
	if r.recoveryErr != nil {
		out := a.output
		if out == "" {
			out = r.lastOutput
		}
		return r.finish(out, fmt.Errorf("%v; same-session nudge failed: %w", r.recoveryErr, attemptErr))
	}
	return r.recoverSameSession(a, attemptErr)
}

// fallbackModel recovers from a provider failure by its named reason: a busy
// provider gets the same model again after a backoff, a spent account gets the
// next model, and a rejected sign-in gets neither another model nor a retry. A
// no-progress stop keeps its own named fallback. No recovery is chosen from the
// text a model wrote.
func (r *journaledRun) fallbackModel(a attempt, attemptErr error) (stepAct, Result, error) {
	if r.ctx.Err() != nil {
		return actNext, Result{}, nil
	}
	reason, typed := attemptFailureReason(a, attemptErr)
	action := reason.Action()
	if !a.noProgress && !typed {
		return actNext, Result{}, nil
	}
	if !a.noProgress && action == providerfailure.ActionReauthenticate {
		// A signed-out account does not improve by another model or another try.
		return actReturn, Result{Output: r.lastOutput, Duration: time.Since(r.start)}, attemptErr
	}
	// A busy provider gets the same model again after a pause, before the
	// model chain counts it as failed.
	retrySame := !a.noProgress && action == providerfailure.ActionRetryLater && r.state.capacityTries < len(capacityBackoff)
	if retrySame {
		if err := capacityWait(r.ctx, capacityBackoff[r.state.capacityTries]); err != nil {
			return actReturn, Result{Output: r.lastOutput, Duration: time.Since(r.start)},
				fmt.Errorf("worker %s canceled/timed out while the model was at capacity: %w", r.p.Name, err)
		}
		r.state.capacityTries++
	} else {
		RecordModelFailure(r.p.Name, r.p.Model, cooldownNow())
	}
	if !retrySame && r.candidateIdx+1 >= len(r.candidates) {
		// The reason's own recovery chain is used up. Finish the turn instead of
		// sending the generic same-session nudge: that would retry a spent account
		// or a busy provider, and its error would lose the reason.
		return actReturn, Result{Output: r.lastOutput, Duration: time.Since(r.start)}, attemptErr
	}
	// Continue any work already done in the exact session. Never
	// restart a turn that may have changed files or called tools.
	// Empty steps changed nothing, so they do not count as work.
	active := r.state.hasActivity()
	reasonText := fallbackReasonText(reason)
	if a.noProgress {
		active, reasonText = r.state.hasRealProgress(), attemptErr.Error()
	}
	sid := r.tracker.idValue()
	if active && (sid == "" || r.inv.resume == nil) {
		return actReturn, Result{Output: r.lastOutput, Duration: time.Since(r.start)}, attemptErr
	}
	if !retrySame {
		if r.session.OnFallback != nil {
			r.session.OnFallback(ModelFallback{From: r.p.Model, To: r.candidates[r.candidateIdx+1], Reason: reasonText})
		}
		r.candidateIdx++
		r.p.Model = r.candidates[r.candidateIdx]
	}
	next, err := buildInvocationInDirExec(r.p, r.prompt, r.cwd, r.exec)
	if err != nil {
		return actReturn, Result{Output: r.lastOutput, Duration: time.Since(r.start)}, err
	}
	if r.inv.cleanup != nil {
		r.inv.cleanup()
	}
	r.inv = next
	r.spec = r.inv.initial
	switch {
	case active && sid != "" && r.inv.resume != nil:
		r.spec = r.inv.resume(sid, sessionNudgePrompt)
	case r.session.ResumeID != "" && r.inv.resume != nil:
		r.spec = r.inv.resume(r.session.ResumeID, r.prompt)
	case r.session.ForkFrom != "" && a.started && sid != "" && r.inv.resume != nil:
		// The fork created its session before the provider refused the first
		// request. Continue that session with the new model; starting the
		// fresh conversation would discard the source history.
		r.spec = r.inv.resume(sid, r.prompt)
	case r.session.ForkFrom != "" && r.inv.fork != nil:
		// The fork never started. Fork the source again, with this
		// invocation's preassigned id.
		r.spec = r.inv.fork(r.session.ForkFrom, r.prompt)
		r.tracker = newSessionTracker(r.inv.sessionID)
		r.tracker.onSession = r.session.OnSession
		r.state.tracker = r.tracker
		r.tracker.observe("")
	default:
		r.tracker = newSessionTracker(r.inv.sessionID)
		r.tracker.onSession = r.session.OnSession
		r.state.tracker = r.tracker
		r.tracker.observe("")
	}
	r.recoveryErr, r.recovered = nil, false
	return actContinue, Result{}, nil
}

// recoverSameSession nudges the exact session once after a failed attempt.
func (r *journaledRun) recoverSameSession(a attempt, attemptErr error) (stepAct, Result, error) {
	// A fresh retry can repeat edits or other side effects. Recover only when
	// the CLI process started and exposed an exact resumable session. A
	// startup-stalled worker produced nothing, so there is nothing to resume.
	if !r.allowRecovery || r.recovered || r.ctx.Err() != nil || !a.started || r.inv.resume == nil || a.startupStalled {
		if r.ctx.Err() == nil {
			return r.finish(r.lastOutput, attemptErr)
		}
		return actReturn, r.result(r.lastOutput), attemptErr
	}
	sessionID := r.tracker.idValue()
	if sessionID == "" {
		return r.finish(r.lastOutput, attemptErr)
	}
	if r.inv.resumeReady != nil {
		if err := r.inv.resumeReady(sessionID); err != nil {
			// The session file does not exist, so the nudge has no target: a
			// real pi exits on the missing session instead of answering. With
			// no real progress to continue from, start the turn's session
			// fresh with the original prompt; otherwise keep the failure.
			if r.state.hasRealProgress() {
				return r.finish(r.lastOutput, attemptErr)
			}
			r.recovered = true
			r.spec = r.inv.initial
			return actContinue, Result{}, nil
		}
	}
	if err := waitForSessionNudge(r.ctx, r.nudgeDelay); err != nil {
		return actReturn, r.result(r.lastOutput),
			fmt.Errorf("worker %s canceled/timed out before session nudge: %w", r.p.Name, err)
	}
	r.recoveryErr = attemptErr
	r.recovered = true
	r.spec = r.inv.resume(sessionID, sessionNudgePrompt)
	return actContinue, Result{}, nil
}

// capacityBackoff lists the pauses before each retry of a capacity error.
// The variables are package-level so tests need no real minutes.
var (
	capacityBackoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}
	capacityWait    = waitForSessionNudge
)

func waitForSessionNudge(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const (
	DeliverySteering = "steering"
	DeliveryJournal  = "journal"

	// DeliveryBoundary: deferred until an in-flight tool call completed.
	DeliveryBoundary = "boundary"
	// DeliveryTurnEnd: the turn finished before a boundary; delivered as the
	// next continuation.
	DeliveryTurnEnd = "turn-end"
	// DeliveryInterrupted: the dialect's stream does not expose tool boundaries,
	// so the delivery waited steering.maxToolWait for the turn to end and then
	// interrupted the worker.
	DeliveryInterrupted = "interrupted"
)

// DeliveryNote describes a steering or journal delivery that did not happen
// immediately, for the run event log.
type DeliveryNote struct {
	Reason  string
	Kind    string
	Outcome string
	Waited  time.Duration
	// InFlight is the number of tool calls still running when it was reported.
	InFlight int
}

type deliveryGate struct {
	maxWait time.Duration
	notify  func(DeliveryNote)
}

func newDeliveryGate(j JournalOptions) *deliveryGate {
	wait := j.MaxToolWait
	if wait == 0 {
		wait = DefaultMaxToolWait
	}
	return &deliveryGate{maxWait: wait, notify: j.OnDelivery}
}

func (g *deliveryGate) report(kind, outcome string, waited time.Duration, inFlight int) {
	if g == nil || g.notify == nil {
		return
	}
	note := DeliveryNote{Kind: kind, Outcome: outcome, Waited: waited, InFlight: inFlight}
	if outcome == DeliveryInterrupted {
		note.Reason = "no-tool-boundaries"
	}
	g.notify(note)
}

// runState carries per-call accounting across invocations: token usage.
type runState struct {
	health        harnesshealth.Observation
	mu            sync.Mutex
	usage         workeroutput.Usage
	tracker       *sessionTracker
	normalizer    *workeroutput.UsageNormalizer
	sealed        bool // a protocol terminal freezes logical accounting during retirement
	emptySteps    int  // step starts in a row with no text, reasoning or tool output
	progressSeen  bool // the worker produced real output, not only step records
	capacityTries int  // pauses already spent on capacity errors
	// startupActivity is set by the first thinking, tool, usage or text event,
	// so a silent-thinking model counts as started even before visible text.
	startupActivity bool
	onCompaction    func()
	// contextTokens is the last per-call prompt size; compactErr is a failed
	// pre-turn compact command.
	contextTokens int64
	compactErr    string
}

func (r *runState) setCompactError(err error) {
	r.mu.Lock()
	r.compactErr = err.Error()
	r.mu.Unlock()
}

func (r *runState) compactValues() (int64, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.contextTokens, r.compactErr
}

// beginProcess marks the start of a new harness process.
func (r *runState) beginProcess() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.normalizer == nil {
		r.normalizer = workeroutput.NewUsageNormalizer("", nil)
	}
	r.sealed = false
	r.emptySteps = 0
	r.startupActivity = false
	r.mu.Unlock()
}

func (r *runState) observeLine(decl *workeroutput.Declaration, line string) {
	r.observeRecord(workeroutput.DecodeLine(decl, line))
}

func (r *runState) observeRecord(record workeroutput.Record) {
	if r == nil {
		return
	}
	if record.Compaction && r.onCompaction != nil {
		r.onCompaction()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return
	}
	r.health.StdoutSeen = true
	if record.ContextTokens > 0 {
		r.contextTokens = record.ContextTokens
	}
	if record.Recognized() {
		r.health.RecognizedRecords++
	}
	for _, event := range record.Events {
		if event.Kind == "status" {
			if event.Name == "step" && event.State == "started" {
				r.emptySteps++
			}
		} else if (event.Kind != "message" && event.Kind != "reasoning") || strings.TrimSpace(event.Text) != "" {
			r.emptySteps, r.progressSeen = 0, true
		}
		r.health.TextSeen = r.health.TextSeen || (event.Kind == "message" && event.Role == "assistant" && strings.TrimSpace(event.Text) != "")
		r.health.ToolSeen = r.health.ToolSeen || event.Kind == "tool"
	}
	r.health.ToolSeen = r.health.ToolSeen || len(record.Signals) > 0
	// Any thinking, tool, usage or text record means the worker started, even
	// when display filtering dropped the events (a textless reasoning start).
	if record.Generates() {
		r.startupActivity = true
	}
	if r.normalizer == nil {
		r.normalizer = workeroutput.NewUsageNormalizer("", nil)
	}
	if u, ok := r.normalizer.ObserveRecord(record); ok {
		r.usage.Add(u)
	}
}

// stepsStalled reports whether the worker started limit steps in a row without
// any text, reasoning or tool output.
func (r *runState) stepsStalled(limit int) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return limit > 0 && r.emptySteps >= limit
}

// hasRealProgress reports output that a new model must continue from.
func (r *runState) hasRealProgress() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progressSeen
}

func (r *runState) hasActivity() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.health.TextSeen || r.health.ToolSeen
}

// hasStartupActivity reports whether the worker produced any harness event at
// all (thinking, tool, usage or text), so a silent-thinking model counts as
// started. A hung worker that emits nothing keeps this false.
func (r *runState) hasStartupActivity() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startupActivity
}

// currentUsage snapshots cumulative logical usage at an observed result.
func (r *runState) currentUsage() workeroutput.Usage {
	if r == nil {
		return workeroutput.Usage{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

func (r *runState) sealUsage(usage workeroutput.Usage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage, r.sealed = usage, true
}

func (r *runState) snapshot() (string, workeroutput.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tracker.idValue(), r.usage
}

func finishAttempt(spec commandSpec, stdout, stderr string, runErr error, started, contextDone, journalInterrupted, steeringInterrupted bool, steering []runstore.SteeringMessage, steeringErr error) attempt {
	var out string
	if spec.parseOutput != nil {
		out = strings.TrimSpace(spec.parseOutput(stdout))
	} else {
		out = strings.TrimSpace(stdout)
	}
	return attempt{
		output: out, stdout: stdout, stderr: stderr, runErr: runErr,
		started: started, contextDone: contextDone, journalInterrupted: journalInterrupted,
		steering: steering, steeringInterrupted: steeringInterrupted, steeringErr: steeringErr,
	}
}

// sessionTracker learns a harness session id from streaming JSON output. This
// is what makes a journal reminder safe: the current turn is
// not interrupted until its exact session is known.
type sessionTracker struct {
	mu        sync.RWMutex
	parse     func(string) string
	id        string
	onSession func(string)
}

// preset pins a known id (a resumed session) before any output is read.
func (s *sessionTracker) preset(id string) {
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
}

func newSessionTracker(parse func(string) string) *sessionTracker {
	return &sessionTracker{parse: parse}
}

// adopt records a session id learned outside stdout parsing, such as an ACP
// session/new reply, and fires the session callback once.
func (s *sessionTracker) adopt(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	fresh := s.id == ""
	if fresh {
		s.id = id
	}
	notify := s.onSession
	s.mu.Unlock()
	if fresh && notify != nil {
		notify(id)
	}
}

func (s *sessionTracker) observe(text string) {
	s.observeRecord(workeroutput.Record{}, text)
}

func (s *sessionTracker) observeRecord(record workeroutput.Record, text string) {
	if s == nil || s.parse == nil {
		return
	}
	s.mu.RLock()
	hasID := s.id != ""
	s.mu.RUnlock()
	if hasID {
		return
	}
	id := record.SessionID
	if id == "" {
		id = s.parse(text)
	}
	s.adopt(id)
}

func (s *sessionTracker) idValue() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// attemptFailureReason returns the reason a trusted boundary named for this
// attempt: a protocol parser, an ACP stop reason, or the process's stderr. The
// text a model wrote never names one, so a turn that merely talks about a
// limit cannot move an account or a model.
func attemptFailureReason(a attempt, attemptErr error) (providerfailure.Reason, bool) {
	if reason, ok := providerfailure.ReasonOf(attemptErr); ok {
		return reason, true
	}
	return providerfailure.ClassifyStderr(a.stderr)
}

// fallbackReasonText says why a model was replaced. It is text for the owner;
// the routing already happened by reason.
func fallbackReasonText(reason providerfailure.Reason) string {
	switch reason {
	case providerfailure.Quota:
		return "the provider rejected the model (quota or rate limit)"
	case providerfailure.Unavailable:
		return "the provider failed on its own side (server error)"
	}
	return "the provider rejected the model"
}

// stderrDetailError carries a worker's standard-error tail beside its cause.
// The tail never joins Error(): the failure card shows the cause, and the tail
// reaches the owner as separate detail on its own.
type stderrDetailError struct {
	err    error
	stderr string
}

func (e *stderrDetailError) Error() string { return e.err.Error() }
func (e *stderrDetailError) Unwrap() error { return e.err }

// StderrDetail returns the worker standard-error tail an error chain carries
// as separate detail, or "". It is evidence for the owner, never cause text.
func StderrDetail(err error) string {
	var d *stderrDetailError
	if errors.As(err, &d) {
		return d.stderr
	}
	return ""
}

func attemptError(ctx context.Context, p profile.Profile, argv []string, a attempt) error {
	// A stopped turn also cancels the steering read; report the stop.
	stopped := a.contextDone && (errors.Is(a.steeringErr, context.Canceled) || errors.Is(a.steeringErr, context.DeadlineExceeded))
	if a.steeringErr != nil && !stopped {
		return fmt.Errorf("worker %s steering source failed: %w", p.Name, a.steeringErr)
	}
	if a.contextDone {
		err := ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		return fmt.Errorf("worker %s canceled/timed out: %w", p.Name, err)
	}
	if a.noProgress {
		return fmt.Errorf("worker %s (model %s) stopped: %w", p.Name, p.Model, a.runErr)
	}
	if a.startupStalled {
		// The startup bound is the cause the owner reads. The worker's stderr
		// is separate detail (StderrDetail), never part of that text, because a
		// harness may print harmless warnings on every turn. The typed reason
		// still comes only from a trusted boundary, never from this display
		// text.
		err := &stderrDetailError{
			err:    fmt.Errorf("worker %s (model %s) failed to start: %w", p.Name, p.Model, a.runErr),
			stderr: tail(strings.TrimSpace(a.stderr), 2000),
		}
		return providerfailure.TagStderr(err, a.stderr)
	}
	var protocolErr error
	switch p.Harness {
	case profile.HarnessPi:
		protocolErr = workeroutput.PiResultError(a.stdout)
	}
	if protocolErr != nil {
		err := fmt.Errorf("worker %s: %w", p.Name, protocolErr)
		// The parser can report an opaque protocol record while the harness
		// already reported why the turn failed. Keep that reason.
		if reason, ok := providerfailure.ReasonOf(a.runErr); ok {
			return providerfailure.Tag(reason, err)
		}
		// Otherwise the process's own stderr is the remaining narrow evidence.
		return providerfailure.TagStderr(err, a.stderr)
	}
	if a.runErr != nil {
		var cwdErr *workingDirectoryError
		if errors.As(a.runErr, &cwdErr) {
			return fmt.Errorf("worker %s cannot start: %w", p.Name, cwdErr)
		}
		errTail := tail(strings.TrimSpace(a.stderr), 2000)
		// Structured stdout is protocol data, and the parsers above already
		// report its errors. Its tail holds model text and usage fields, such
		// as "fallback_credit", that look like provider failures. A plain text
		// harness has no parser, so its tail stays in the message for the
		// owner; the reason still comes only from a parser or from stderr.
		if errTail == "" && p.EffectiveDialect() == workeroutput.DialectText {
			errTail = tail(strings.TrimSpace(a.stdout), 2000)
		}
		var err error
		if errTail == "" {
			err = fmt.Errorf("worker %s (%s) failed: %w", p.Name, commandName(argv), a.runErr)
		} else {
			err = fmt.Errorf("worker %s (%s) failed: %w: %s", p.Name, commandName(argv), a.runErr, errTail)
		}
		// A process's own stderr is the only text a turn may name a reason from.
		return providerfailure.TagStderr(err, a.stderr)
	}
	if a.output == "" && !a.allowEmptyOutput {
		return fmt.Errorf("worker %s returned empty output", p.Name)
	}
	return nil
}

// cleanEmptyStop reports a turn the harness itself ended cleanly with no final
// text. An agent is told to answer nothing when it owes no report, so this is
// a finished turn, not a transient failure to recover from.
func cleanEmptyStop(p profile.Profile, stdout string) bool {
	switch p.Harness {
	case profile.HarnessPi:
		return workeroutput.PiCleanStop(stdout)
	case profile.HarnessClaudeCode:
		return workeroutput.ClaudeCleanStop(stdout)
	}
	return false
}

// commandName names the launched program; an ACP attempt runs no process.
func commandName(argv []string) string {
	if len(argv) == 0 {
		return "acp"
	}
	return argv[0]
}

func claudeStreamOutput(stdout string) string {
	result, _ := workeroutput.ClaudeStreamResult(stdout)
	return result
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasAnyOption(args []string, options ...string) bool {
	for _, arg := range args {
		for _, option := range options {
			if arg == option || strings.HasPrefix(arg, option+"=") {
				return true
			}
		}
	}
	return false
}

func argumentValue(args []string, option string) (string, bool) {
	for i, arg := range args {
		if arg == option {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(arg, option+"=") {
			return strings.TrimPrefix(arg, option+"="), true
		}
	}
	return "", false
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
