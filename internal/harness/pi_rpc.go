package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

const (
	piCommandTimeout = 30 * time.Second
	piAbortGrace     = 10 * time.Second
)

// piPromptTimeout bounds the turn's main prompt command, which can follow a
// fresh process start: under load (dozens of sessions, a network flap) pi
// needs well over 30 s to create a session before it answers (D-221). The
// startup watchdog still bounds a silent process. A var, so tests can
// shorten it.
var piPromptTimeout = 2 * time.Minute

type piResponse struct {
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

// piRPCClient owns one attachment's command responses and event queue.
// The shared session host owns the process and its lifetime.
type piRPCClient struct {
	sess    sessionhost.Session
	sig     chan struct{}
	done    chan struct{}
	stop    context.CancelFunc
	mu      sync.Mutex
	queued  []string
	pending map[string]chan piResponse
	nextID  int
	err     error
}

func newPiRPCClient(sess sessionhost.Session) *piRPCClient {
	c := &piRPCClient{
		sess: sess, sig: make(chan struct{}, 1), done: make(chan struct{}),
		pending: map[string]chan piResponse{},
	}
	var ctx context.Context
	ctx, c.stop = context.WithCancel(context.Background())
	go c.pump(ctx)
	return c
}

func (c *piRPCClient) wake() {
	select {
	case c.sig <- struct{}{}:
	default:
	}
}

// signal wakes the turn loop when frames are queued.
func (c *piRPCClient) signal() <-chan struct{} { return c.sig }

// done closes when the pump ended: the child exited or the client stopped.
func (c *piRPCClient) exited() <-chan struct{} { return c.done }

// take returns every queued event frame.
func (c *piRPCClient) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queued) == 0 {
		return nil
	}
	lines := c.queued
	c.queued = nil
	return lines
}

func (c *piRPCClient) pump(ctx context.Context) {
	defer close(c.done)
	for {
		frame, err := c.sess.Next(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.fail(err)
			}
			return
		}
		if c.route(frame) {
			continue
		}
		c.mu.Lock()
		c.queued = append(c.queued, string(frame))
		c.mu.Unlock()
		c.wake()
	}
}

// route resolves one response record to its waiting command; every other
// frame is an event for the turn loop.
func (c *piRPCClient) route(frame []byte) bool {
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(frame, &head) != nil || head.Type != "response" {
		return false
	}
	c.mu.Lock()
	ch := c.pending[head.ID]
	delete(c.pending, head.ID)
	c.mu.Unlock()
	if ch == nil {
		return true
	}
	var resp piResponse
	_ = json.Unmarshal(frame, &resp)
	ch <- resp
	return true
}

func (c *piRPCClient) fail(err error) {
	c.mu.Lock()
	c.err = err
	pending := c.pending
	c.pending = map[string]chan piResponse{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- piResponse{Success: false, Error: err.Error()}
	}
}

// command sends one RPC record and waits for its response. timeout <= 0
// waits as long as ctx allows.
func (c *piRPCClient) command(ctx context.Context, record map[string]any, timeout time.Duration) (piResponse, error) {
	c.mu.Lock()
	id := fmt.Sprintf("dyna-%d", c.nextID)
	c.nextID++
	ch := make(chan piResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	drop := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	record["id"] = id
	payload, err := json.Marshal(record)
	if err != nil {
		drop()
		return piResponse{}, err
	}
	if err := c.sess.Write(payload); err != nil {
		drop()
		return piResponse{}, err
	}
	wait := func() (piResponse, error) {
		select {
		case resp := <-ch:
			return resp, nil
		case <-c.done:
			drop()
			return piResponse{}, c.exitError()
		case <-ctx.Done():
			drop()
			return piResponse{}, ctx.Err()
		}
	}
	if timeout <= 0 {
		return wait()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp, nil
	case <-c.done:
		drop()
		return piResponse{}, c.exitError()
	case <-ctx.Done():
		drop()
		return piResponse{}, ctx.Err()
	case <-timer.C:
		drop()
		return piResponse{}, fmt.Errorf("pi rpc %v command timed out after %s", record["type"], timeout)
	}
}

// stopPump ends the frame pump. Pending commands fail through their wait.
func (c *piRPCClient) stopPump() {
	c.stop()
	<-c.done
}

func (c *piRPCClient) exitError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("pi rpc session ended")
}

// piSessionArg returns the session named by a pi rpc argv: --session-id
// preassigns a fresh session, --session opens an existing one.
func piSessionArg(argv []string) string {
	for i := 1; i < len(argv); i++ {
		if (argv[i] == "--session" || argv[i] == "--session-id") && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// piOptionValue returns the value after option, in "--opt value" or
// "--opt=value" form.
func piOptionValue(argv []string, option string) string {
	for i := 1; i < len(argv); i++ {
		if argv[i] == option && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(argv[i], option+"=") {
			return strings.TrimPrefix(argv[i], option+"=")
		}
	}
	return ""
}

// piSessionIdentity reduces a selector to the session id. A resume by file
// path names the same session as the id in its file name; keeping the id makes
// a fresh turn and a resume share one RPC process. Pi stores a session as
// <time>_<id>.jsonl; a path that does not follow that shape stays as it is.
func piSessionIdentity(selector string) string {
	if !strings.HasSuffix(selector, ".jsonl") {
		return selector
	}
	name := strings.TrimSuffix(filepath.Base(selector), ".jsonl")
	if index := strings.Index(name, "_"); index >= 0 && index+1 < len(name) {
		return name[index+1:]
	}
	return selector
}

// piSessionHome names the home that stores the conversation: --session-dir
// (or the agent dir) differs per account, so the same conversation in two
// homes is two sessions.
func piSessionHome(argv, env []string) string {
	home := piOptionValue(argv, "--session-dir")
	if home == "" {
		home = environmentValue(env, "PI_CODING_AGENT_SESSION_DIR")
	}
	if home == "" {
		home = environmentValue(env, "PI_CODING_AGENT_DIR")
	}
	return home
}

// piLaunchKey scopes the caller's session key to the home that stores the
// conversation. The key must stay the caller's identity, never a native
// protocol id: session close, transfer and idle relief find a session's
// child by that key, and a native id leaves the child running under its host
// with its memory and its runtime bundle lease (D-399).
func piLaunchKey(caller string, argv, env []string) string {
	return caller + "\x00" + piSessionHome(argv, env)
}

// piLaunchFingerprint identifies a process's launch identity: executable,
// environment, and every argument except the session selector. A resumed
// turn reuses the process only when this matches, so an account switch, a
// model change, or an updated bundle restarts it on the same session.
func piLaunchFingerprint(executable string, env, argv []string) string {
	joined := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if (argv[i] == "--session" || argv[i] == "--session-id") && i+1 < len(argv) {
			// The selector names the session, not the launch; a fresh
			// --session-id and a resumed --session of one session share a
			// process. Drop the pair so their order cannot differ either.
			i++
			continue
		}
		joined = append(joined, argv[i])
	}
	return sessionLaunchFingerprint(executable, env, joined)
}

// piSessionLaunch builds the host launch for one Pi turn. An anonymous run
// with no Dyna identity keys by its native conversation id, the one stable
// id it has; a session's turn keys by the session, so its close and transfer
// stop the child. The native conversation id rides the fingerprint, so a
// fresh conversation and a resume of the same one share a child, while two
// conversations of one session replace it instead of reusing each other's.
func piSessionLaunch(p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (sessionhost.Launch, error) {
	launch, err := nativeSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return sessionhost.Launch{}, err
	}
	caller := piLaunchCaller(p, execution, spec)
	launch.Key = piLaunchKey(caller, spec.argv, launch.Env)
	launch.Fingerprint = piSessionIdentity(piSessionArg(spec.argv)) + "\x00" + piLaunchFingerprint(launch.Argv[0], launch.Env, spec.argv) + "\x00" + cwd
	return launch, nil
}

// piLaunchCaller names the launch's caller: the session or run that runs the
// turn, or the native conversation when no Dyna identity exists.
func piLaunchCaller(p profile.Profile, execution ExecutionOptions, spec commandSpec) string {
	if key := sessionKey(p, execution); key != "" {
		return key
	}
	return piSessionIdentity(piSessionArg(spec.argv))
}

// piSessionProcessFor attaches the Pi codec to the shared process host.
func piSessionProcessFor(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (sessionhost.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	launch, err := piSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return nil, err
	}
	return nativeSessionHost.Session(ctx, launch)
}

// runPiTurn runs one pi turn over the session's long-lived rpc process: it
// attaches to the frame stream, sends the prompt as an RPC command, delivers
// steering and journal reminders as queued prompts, and ends at
// agent_settled. The process stays alive for the next turn; only a context
// cancel or a failed run stops it, by RPC abort first.
func runPiTurn(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, liveLine func(string), gate *deliveryGate, state *runState, execution ExecutionOptions) (out attempt) {
	streamNudges := 0
	streamSteered := false
	defer func() { out.streamNudges, out.streamSteered = streamNudges, streamSteered }()

	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, spec.prompt); err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	sess, err := piSessionProcessFor(ctx, p, cwd, spec, execution)
	if err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	detachReason := sessionhost.DetachAbandoned
	defer func() { sess.Detach(detachReason) }()
	client := newPiRPCClient(sess)
	defer client.stopPump()
	var liveOutput, normalizedOutput *os.File
	if outputPath != "" {
		liveOutput, _ = os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0)
	}
	if liveOutput != nil {
		defer liveOutput.Close()
	}
	if normalizedOutputPath != "" {
		normalizedOutput, _ = os.OpenFile(normalizedOutputPath, os.O_APPEND|os.O_WRONLY, 0)
	}
	if normalizedOutput != nil {
		defer normalizedOutput.Close()
	}
	normalizer := workeroutput.NewNormalizer(output, normalizedOutput)
	tools := workeroutput.NewToolTracker(output)
	boundary := make(chan struct{}, 1)
	sessionReady := make(chan struct{}, 1)
	var sessionObserved sync.Once
	emptyLimit := emptyStepLimit
	if execution.emptyStepLimit > 0 {
		emptyLimit = execution.emptyStepLimit
	}
	noProgress := make(chan struct{}, 1)
	settled := make(chan struct{}, 1)
	promptAccepted := false
	observeLine := func(line string) {
		if liveLine != nil {
			liveLine(line)
		}
		record := workeroutput.DecodeLine(output, line)
		tracker.observeRecord(record, line)
		if tracker.idValue() != "" {
			sessionObserved.Do(func() { sessionReady <- struct{}{} })
		}
		state.observeRecord(record)
		if state.stepsStalled(emptyLimit) {
			select {
			case noProgress <- struct{}{}:
			default:
			}
		}
		_ = normalizer.WriteRecord(record)
		if tools.ObserveSignals(record.Signals) {
			select {
			case boundary <- struct{}{}:
			default:
			}
		}
		if promptAccepted {
			for _, event := range record.Events {
				if event.Kind == "status" && event.Name == "agent_settled" {
					select {
					case settled <- struct{}{}:
					default:
					}
				}
			}
		}
	}
	stdout := newObservedBuffer(observeLine, liveOutput)

	state.beginProcess()

	paused := func() bool { return steering != nil && steering.opts.Paused != nil && steering.opts.Paused() }
	finishTurn := func(runErr error, contextDone bool) attempt {
		if journal != nil {
			journal.poll()
		}
		var messages []runstore.SteeringMessage
		if steering != nil {
			_ = steering.activateIfReady(tracker.idValue())
			steering.poll()
			messages = steering.take()
		}
		return finishAttempt(spec, stdout.String(), nativeSessionStderr(sess), runErr, true, contextDone, false, false, messages, steering.errValue())
	}
	// abortRun asks pi to stop the run, so no tool keeps executing unwatched
	// after supervision ends. Pi's abort response itself proves the session
	// is idle again (pi waits for idle before responding), so a successful
	// abort only needs to drain the buffered final records; a lost, refused
	// or timed-out abort stops the process group.
	abortRun := func() {
		abortCtx, cancel := context.WithTimeout(context.Background(), piAbortGrace)
		defer cancel()
		resp, err := client.command(abortCtx, map[string]any{"type": "abort"}, 0)
		if err != nil || !resp.Success {
			haltNativeSession(sess)
			return
		}
		detachReason = sessionhost.DetachIdle
		for _, line := range client.take() {
			_, _ = io.WriteString(stdout, line+"\n")
		}
	}

	var startBoundary *int64
	sendPrompt := func() error {
		startBoundary = normalizedBoundary(normalizer)
		resp, err := client.command(ctx, map[string]any{"type": "prompt", "message": spec.prompt}, piPromptTimeout)
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("pi rejected the prompt: %s", resp.Error)
		}
		promptAccepted = true
		return nil
	}
	var promptErr error
	if steering != nil && steering.opts.StartGate != nil {
		promptErr = steering.opts.StartGate(ctx, spec.prompt, sendPrompt)
	} else {
		promptErr = sendPrompt()
	}
	if promptErr != nil {
		// The prompt command may already be in pi's hands when its handshake
		// fails - a cancel between the write and the response, a timeout, an
		// exit - and pi starts the run without awaiting preflight. An abort
		// then acknowledges current idleness without canceling that pending
		// prompt, which could start a run after supervision returns. No
		// completion was positively established, so stop the run and the
		// process; the next attempt resumes the same session file.
		detachReason = sessionhost.DetachAbandoned
		abortRun()
		haltNativeSession(sess)
		return finishTurn(promptErr, ctx.Err() != nil)
	}
	if steering != nil {
		if steering.opts.OnStart != nil {
			steering.opts.OnStart()
		}
		if steering.opts.OnMessage != nil {
			for _, message := range spec.steering {
				steering.opts.OnMessage(message, startBoundary)
			}
		}
	}

	// deliverSteering queues pending steering on the running session. Pi
	// delivers a queued prompt after the current tool batch, before the next
	// model call, so no interrupt is needed.
	deliverSteering := func() error {
		if paused() || !steering.hasPending() {
			return nil
		}
		// A queued prompt sent after the run settled would start an unwatched
		// new run; when the turn just ended, the machine resumes pending
		// steering as the next continuation prompt instead.
		select {
		case <-settled:
			return nil
		default:
		}
		messages := steering.take()
		if err := steering.dispatch(messages); err != nil {
			return fmt.Errorf("record steering dispatch for worker %s: %w", p.Name, err)
		}
		if journal != nil {
			journal.noteExternalResume()
		}
		if paused() {
			steering.pending = append(messages, steering.pending...)
			return nil
		}
		var sourceOffset *int64
		send := func() error {
			sourceOffset = normalizedBoundary(normalizer)
			record := map[string]any{"type": "prompt", "message": steeringPrompt(messages), "streamingBehavior": "steer"}
			resp, err := client.command(ctx, record, piCommandTimeout)
			if err != nil {
				return err
			}
			if !resp.Success {
				return fmt.Errorf("pi rejected the steering prompt: %s", resp.Error)
			}
			return nil
		}
		var deliveryErr error
		if steering.opts.DeliveryGate != nil {
			deliveryErr = steering.opts.DeliveryGate(send)
		} else {
			deliveryErr = send()
		}
		if deliveryErr != nil {
			steering.pending = append(messages, steering.pending...)
			return deliveryErr
		}
		if steering.opts.OnMessage != nil {
			for _, message := range messages {
				steering.opts.OnMessage(message, sourceOffset)
			}
		}
		gate.report(DeliverySteering, DeliveryBoundary, 0, tools.InFlight())
		streamSteered = true
		return nil
	}
	// nudgeJournal queues an idle journal reminder the same way; the worker
	// answers it inside this run, without an interrupt.
	nudgeJournal := func() {
		if journal == nil || !journal.reminderDue() || paused() {
			return
		}
		select {
		case <-settled:
			return
		default:
		}
		record := map[string]any{"type": "prompt", "message": journalNudgePrompt, "streamingBehavior": "steer"}
		resp, err := client.command(ctx, record, piCommandTimeout)
		if err != nil || !resp.Success {
			// Best effort: the completion reminder after the turn still
			// covers a worker that ignored the nudge.
			journal.markNudged(false, JournalNudgeIdle)
			return
		}
		journal.noteStreamNudge()
		streamNudges++
	}

	var sourceDoorbell steeringWake
	if steering != nil {
		sourceDoorbell, _ = steering.source.(steeringWake)
	}
	interval := workerSteeringPollInterval
	if sourceDoorbell != nil {
		interval = steeringDeliveryInterval
	}
	if steering != nil && steering.opts.pollEvery > 0 {
		interval = steering.opts.pollEvery
	}
	var tick <-chan time.Time
	var ticker *time.Ticker
	if journal != nil || steering != nil {
		if journal != nil && journal.pollInterval() < interval {
			interval = journal.pollInterval()
		}
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}

	startupWait := StartupLimit
	if execution.startupLimit > 0 {
		startupWait = execution.startupLimit
	}
	startupTimer := time.NewTimer(startupWait)
	defer startupTimer.Stop()
	startupC := startupTimer.C
	if execution.startupFired != nil {
		startupC = execution.startupFired
	}

	poll := func(readInput bool) (attempt, bool) {
		if steering != nil {
			_ = steering.activateIfReady(tracker.idValue())
			if readInput {
				steering.poll()
			}
			if steering.errValue() != nil {
				detachReason = sessionhost.DetachAbandoned
				abortRun()
				return finishTurn(steering.errValue(), false), true
			}
			if err := deliverSteering(); err != nil {
				detachReason = sessionhost.DetachAbandoned
				abortRun()
				return finishTurn(err, false), true
			}
		}
		if paused() {
			return attempt{}, false
		}
		if journal != nil {
			journal.poll()
			nudgeJournal()
		}
		return attempt{}, false
	}

	for {
		var inputWake <-chan struct{}
		if sourceDoorbell != nil {
			_ = steering.activateIfReady(tracker.idValue())
			if steering.active {
				inputWake = sourceDoorbell.Wake()
			}
		}
		select {
		case <-settled:
			detachReason = sessionhost.DetachIdle
			return finishTurn(nil, false)
		case <-client.signal():
			for _, line := range client.take() {
				_, _ = io.WriteString(stdout, line+"\n")
			}
			select {
			case <-settled:
				detachReason = sessionhost.DetachIdle
				return finishTurn(nil, false)
			default:
			}
		case <-client.exited():
			for _, line := range client.take() {
				_, _ = io.WriteString(stdout, line+"\n")
			}
			select {
			case <-settled:
				detachReason = sessionhost.DetachIdle
				return finishTurn(nil, false)
			default:
			}
			err := fmt.Errorf("pi rpc process ended: %w", client.exitError())
			return finishTurn(err, ctx.Err() != nil)
		case <-ctx.Done():
			detachReason = sessionhost.DetachAbandoned
			if execution.KillOnCancel {
				killNativeSession(sess)
				<-client.exited()
				return finishTurn(ctx.Err(), true)
			}
			abortRun()
			return finishTurn(ctx.Err(), true)
		case <-noProgress:
			detachReason = sessionhost.DetachAbandoned
			abortRun()
			a := finishTurn(nil, false)
			a.noProgress = true
			a.runErr = fmt.Errorf("no output after %d empty steps in a row", emptyLimit)
			return a
		case <-startupC:
			if state.hasStartupActivity() {
				// The worker started; a slow first thought is not a hang.
				continue
			}
			detachReason = sessionhost.DetachAbandoned
			abortRun()
			a := finishTurn(nil, false)
			a.startupStalled = true
			a.runErr = fmt.Errorf("no harness event within %s of startup", startupWait)
			return a
		case <-boundary:
			if result, ok := poll(false); ok {
				return result
			}
		case <-sessionReady:
			if result, ok := poll(false); ok {
				return result
			}
		case <-inputWake:
			if result, ok := poll(true); ok {
				return result
			}
		case <-tick:
			if result, ok := poll(sourceDoorbell == nil || inputWake == nil); ok {
				return result
			}
		}
	}
}

// runPiCompact runs the session's compaction over its long-lived rpc
// process, admitting the attempt through the same gates as a turn.
func runPiCompact(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, steering *steeringSupervisor, execution ExecutionOptions, admit func(context.Context) error) error {
	if err := admit(ctx); err != nil {
		return err
	}
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, ""); err != nil {
			return err
		}
	}
	sess, err := piSessionProcessFor(ctx, p, cwd, spec, execution)
	if err != nil {
		return err
	}
	reason := sessionhost.DetachAbandoned
	defer func() { sess.Detach(reason) }()
	client := newPiRPCClient(sess)
	defer client.stopPump()
	resp, err := client.command(ctx, map[string]any{"type": "compact"}, 0)
	if err != nil {
		return fmt.Errorf("pi compact: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("pi compact: %s", resp.Error)
	}
	reason = sessionhost.DetachIdle
	return nil
}
