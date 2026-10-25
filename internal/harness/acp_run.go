package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/acp"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/jsonrpc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// Grok and Antigravity share ACP over one stdio child per Dyna session.
// The session host owns each child; this codec owns its JSON-RPC connection.

// acpCancelWait bounds how long Dyna waits for the agent to confirm a cancel
// before it stops that session child. Tests shorten the protocol grace.
var acpCancelWait = 30 * time.Second

// validateGrokAcpArgs rejects profile flags that only exist on grok's
// headless-print path. Generated account flags remain on the stdio command.
func validateGrokAcpArgs(p profile.Profile) error {
	if hasAnyOption(p.ExtraArgs, "--leader", "--output-format", "--single", "-p", "--prompt", "--prompt-file", "--prompt-json", "--session-id", "-s", "--resume", "-r", "--continue", "-c", "--fork-session", "--json-schema", "--input-format", "--no-memory", "--no-subagents", "--no-plan", "--disallowed-tools", "--max-turns", "-m", "--model", "--always-approve", "--permission-mode") {
		return fmt.Errorf("grok extraArgs conflict with the ACP agent path; dyna reserves session, prompt, model, permission and headless-print options")
	}
	return nil
}

// acpSession attaches one turn to a hosted native session.
type acpSession struct {
	conn        *jsonrpc.Conn
	agent       *acp.Agent
	session     string
	model       string
	host        sessionhost.Session
	antigravity bool
	settled     bool
}

func (s *acpSession) close() {
	if s.conn != nil {
		s.conn.Close()
	}
	if s.host == nil {
		return
	}
	// Close already ends the attachment; only a settled shared child detaches.
	if !s.settled {
		_ = s.host.Close()
	} else {
		s.host.Detach(sessionhost.DetachIdle)
	}
	s.host = nil
}

// acpDial initializes cold children and resolves the exact native session.
func acpDial(ctx context.Context, p profile.Profile, cwd, sessionID string, execution ExecutionOptions, onUpdate func(acp.Update), onStderr func(string)) (*acpSession, error) {
	argv, err := acpArgv(p)
	if err != nil {
		return nil, err
	}
	if p.Harness == profile.HarnessAntigravity {
		if err := validateAntigravityACPBundle(p.RuntimeExecutable); err != nil {
			return nil, err
		}
		env := make(map[string]string, len(p.Env))
		for k, v := range p.Env {
			env[k] = v
		}
		for k, v := range harnessruntime.AntigravityACPEnv(p.RuntimeExecutable, p.Env["HOME"]) {
			env[k] = v
		}
		p.Env = env
		p.RuntimeExecutable = harnessruntime.AntigravityACPExecutable(p.RuntimeExecutable)
	}
	launch, err := nativeSessionLaunch(p, cwd, commandSpec{argv: argv}, execution)
	if err != nil {
		return nil, err
	}
	home := p.Env["GROK_HOME"]
	if p.Harness == profile.HarnessAntigravity {
		home = p.Env["HOME"]
	}
	if p.Harness == profile.HarnessKimiCode {
		home = p.Env["KIMI_CODE_HOME"]
	}
	launch.Key += "\x00" + home
	cold := false
	started := launch.OnStart
	launch.OnStart = func(pid int) {
		cold = true
		if started != nil {
			started(pid)
		}
	}
	child, err := nativeSessionHost.Session(ctx, launch)
	if err != nil {
		return nil, err
	}
	s := &acpSession{host: child, antigravity: p.Harness == profile.HarnessAntigravity}

	handlers := acp.Handlers{Update: onUpdate, Stderr: onStderr, Permission: func(context.Context, acp.PermissionRequest) acp.PermissionResponse {
		return acp.PermissionResponse{Behavior: acp.PermissionCancelled}
	}}
	s.conn = acp.AttachTransport(newNativeSessionTransport(child), handlers)
	handshakeCtx, hcancel := context.WithTimeout(ctx, 60*time.Second)
	defer hcancel()
	s.agent = acp.ConnectedAgent(s.conn)
	if cold {
		version := 1
		if s.antigravity {
			version = 2
		}
		s.agent, err = acp.HandshakeVersion(handshakeCtx, s.conn, version)
		if err == nil {
			method := s.agent.DefaultAuthMethod()
			if s.antigravity {
				method = "oauth-personal"
			}
			err = s.agent.Authenticate(handshakeCtx, method)
		}
		if err != nil {
			s.close()
			return nil, err
		}
	}
	if s.antigravity {
		s.agent.SessionMeta = antigravitySessionMeta(harnessruntime.NativeFeatureDenyTools(p.Harness, execution.DynaShell))
	}
	var setup acp.SessionSetup
	servers, err := acpMCPServers(p, launch.Env)
	if err != nil {
		s.close()
		return nil, err
	}
	if sessionID != "" {
		s.conn.SetNotification(func(jsonrpc.Notification) {})
		if s.antigravity {
			setup, err = s.agent.ResumeSession(handshakeCtx, sessionID, cwd, servers)
		} else {
			setup, err = s.agent.LoadSession(handshakeCtx, sessionID, cwd, servers)
		}
		s.conn.SetNotification(acp.NotificationHandler(onUpdate))
		s.session = sessionID
	} else {
		setup, err = s.agent.NewSession(handshakeCtx, cwd, servers)
		s.session = setup.SessionID
	}
	if err != nil {
		s.close()
		return nil, err
	}
	if s.antigravity || p.Harness == profile.HarnessKimiCode {
		mode := "yolo"
		if p.SafeMode {
			mode = "default"
		}
		if err := s.agent.SetMode(handshakeCtx, s.session, mode); err != nil {
			s.close()
			return nil, err
		}
	}
	if model, ok := setup.CurrentModel(); ok {
		s.model = model
	}
	return s, nil
}

func acpArgv(p profile.Profile) ([]string, error) {
	if p.Harness == profile.HarnessKimiCode {
		if p.Env["KIMI_CODE_HOME"] == "" {
			return nil, errors.New("Kimi Code ACP needs the private KIMI_CODE_HOME")
		}
		return []string{"kimi", "acp"}, nil
	}
	if p.Harness == profile.HarnessAntigravity {
		if err := validateAntigravityACPLogin(p.Env["HOME"]); err != nil {
			return nil, err
		}
		return []string{"agy", "--uid="}, nil
	}
	if p.Env["GROK_HOME"] == "" {
		return nil, errors.New("Grok ACP needs the private GROK_HOME")
	}
	if err := validateGrokAcpArgs(p); err != nil {
		return nil, err
	}
	// Grok accepts --no-auto-update only before the subcommand.
	argv := []string{"grok"}
	var agentArgs []string
	for _, arg := range p.ExtraArgs {
		if arg == "--no-auto-update" {
			argv = append(argv, arg)
		} else {
			agentArgs = append(agentArgs, arg)
		}
	}
	argv = append(argv, "agent")
	// The managed generation already passes the private leader flags, and
	// Grok refuses a repeated --no-leader.
	if !slices.Contains(agentArgs, "--no-leader") {
		argv = append(argv, "--no-leader", "--leader-socket", filepath.Join(p.Env["GROK_HOME"], "leader.sock"))
	}
	argv = append(argv, agentArgs...)
	if !p.SafeMode {
		argv = append(argv, "--always-approve")
	}
	return append(argv, "stdio"), nil
}

// acpPromptState carries one in-flight prompt and its submission sequence.
type acpPromptState struct {
	seq int
	err error
	res acp.PromptResult
}

// runAcpAttempt runs one prompt over its hosted ACP child with
// the same supervision as a process turn: journal reminders, steering,
// startup bounds and tool-boundary delivery. Steering is native: a queued
// session/prompt the agent answers after the current work. Journal reminders
// and cancellation interrupt through session/cancel.
func runAcpAttempt(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, resumable bool, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, liveLine func(string), gate *deliveryGate, state *runState, execution ExecutionOptions) attempt {
	if err := validateWorkingDirectory(cwd); err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	if spec.acpPrompt == "" {
		return finishAttempt(spec, "", "", errors.New("acp attempt without a prompt"), false, false, false, false, nil, nil)
	}
	var rawLines boundedLines
	var stderr acpStderrBuffer
	if output == nil {
		output = p.EffectiveOutput()
	}
	tools := workeroutput.NewToolTracker(output)
	boundary := make(chan struct{}, 1)
	accumulator := workeroutput.AcpOutput()
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
	observe := func(line string) {
		rawLines.add(line)
		if liveOutput != nil {
			_, _ = liveOutput.WriteString(line + "\n")
		}
		if liveLine != nil {
			liveLine(line)
		}
		record := workeroutput.DecodeLine(output, line)
		tracker.observeRecord(record, line)
		state.observeRecord(record)
		_ = normalizer.WriteRecord(record)
		if tools.ObserveSignals(record.Signals) {
			select {
			case boundary <- struct{}{}:
			default:
			}
		}
		// The accumulator tracks each chunk's prompt id itself, so a queued
		// steering prompt resets the answer it collects.
		accumulator.Observe(line)
	}
	onUpdate := func(u acp.Update) {
		if u.Raw != nil {
			// The decoder and accumulator read the full method/params envelope.
			observe(string(u.Raw))
		}
	}
	// The prompt send is gated like a process start: the room can pause it.
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, spec.acpPrompt); err != nil {
			return finishAttempt(spec, rawLines.String(), stderr.String(), err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	s, err := acpDial(ctx, p, cwd, tracker.idValue(), execution, onUpdate, stderr.line)
	if err != nil {
		return finishAttempt(spec, rawLines.String(), stderr.String(), err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	defer s.close()
	if s.session == "" {
		return finishAttempt(spec, rawLines.String(), stderr.String(), errors.New("acp session missing"), false, false, false, false, nil, nil)
	}
	if tracker.idValue() == "" {
		tracker.adopt(s.session)
	}
	model, effort := p.Model, ""
	if p.Harness == profile.HarnessKimiCode {
		model, effort = splitKimiModelEffort(model)
	}
	if model != "" && model != s.model {
		if err := s.agent.SetModel(ctx, s.session, model); err != nil {
			return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, ctx.Err() != nil, false, false, nil, nil)
		}
		s.model = model
	}
	if effort != "" {
		if err := s.agent.SetConfigOption(ctx, s.session, "thinking", effort); err != nil {
			return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	if steering != nil {
		if err := steering.activateIfReady(tracker.idValue()); err != nil {
			return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, false, false, false, nil, err)
		}
	}
	state.beginProcess()
	accumulator.Reset()

	promptDone := make(chan acpPromptState, 4)
	inFlight := 0
	submitted := 0
	settled := map[int]acpPromptState{}
	// Prompts run on their own context, detached from the turn: a canceled
	// turn must still see the agent's real reply (or its silence) while the
	// cancel is confirmed, instead of a local context error.
	promptCtx, cancelPrompts := context.WithCancel(context.Background())
	defer cancelPrompts()
	send := func(text string) {
		submitted++
		seq := submitted
		go func() {
			res, err := s.agent.Prompt(promptCtx, s.session, []acp.PromptBlock{acp.TextBlock(text)})
			select {
			case promptDone <- acpPromptState{seq: seq, err: err, res: res}:
			case <-promptCtx.Done():
			}
		}()
		inFlight++
	}
	// cancelAndDrain interrupts every in-flight prompt and waits for the
	// agent's confirmation on its own clock; an unconfirmed cancel stops the
	// session child so its sessions fall back to a cold restore.
	cancelAndDrain := func() error {
		if inFlight == 0 {
			return nil
		}
		if s.conn.Done() {
			// A broken connection cannot confirm the prompt stopped.
			_ = s.host.Close()
			return nil
		}
		_ = s.agent.Cancel(s.session)
		grace := time.NewTimer(acpCancelWait)
		defer grace.Stop()
		for inFlight > 0 {
			select {
			case st := <-promptDone:
				inFlight--
				if st.err != nil {
					// Remote completion is unconfirmable after a transport
					// failure; the agent process is stopped rather than
					// trusted to have ended the prompt on its own.
					_ = s.host.Close()
					return st.err
				}
			case <-grace.C:
				_ = s.host.Close()
				return fmt.Errorf("%s ACP agent did not confirm the interrupt within %s", p.Harness, acpCancelWait)
			}
		}
		s.settled = true
		return nil
	}
	var startBoundary *int64
	sendInitial := func() error {
		startBoundary = normalizedBoundary(normalizer)
		send(spec.acpPrompt)
		return nil
	}
	if steering != nil && steering.opts.StartGate != nil {
		if err := steering.opts.StartGate(ctx, spec.acpPrompt, sendInitial); err != nil {
			return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, ctx.Err() != nil, false, false, nil, nil)
		}
	} else {
		_ = sendInitial()
	}
	if steering != nil && steering.opts.OnStart != nil {
		steering.opts.OnStart()
	}
	if steering != nil && steering.opts.OnMessage != nil {
		for _, message := range spec.steering {
			steering.opts.OnMessage(message, startBoundary)
		}
	}
	if spec.journalReminderReason != "" && journal != nil {
		journal.markNudged(true, spec.journalReminderReason)
	}

	var sourceDoorbell steeringWake
	if steering != nil {
		sourceDoorbell, _ = steering.source.(steeringWake)
	}
	var tick <-chan time.Time
	var ticker *time.Ticker
	if journal != nil || steering != nil {
		interval := workerSteeringPollInterval
		if sourceDoorbell != nil {
			interval = steeringDeliveryInterval
		}
		if steering != nil && steering.opts.pollEvery > 0 {
			interval = steering.opts.pollEvery
		}
		if journal != nil && journal.pollInterval() < interval {
			interval = journal.pollInterval()
		}
		ticker = time.NewTicker(interval)
		tick = ticker.C
		defer ticker.Stop()
	}
	startupWait := startupLimit
	if execution.startupLimit > 0 {
		startupWait = execution.startupLimit
	}
	startupTimer := time.NewTimer(startupWait)
	defer startupTimer.Stop()
	startupC := startupTimer.C
	if execution.startupFired != nil {
		startupC = execution.startupFired
	}
	var journalInterrupted bool
	streamSteered := false
	deferredSince := map[string]time.Time{}
	reportDeferred := func(outcome string) {
		for kind, since := range deferredSince {
			gate.report(kind, outcome, time.Since(since), tools.InFlight())
		}
		clear(deferredSince)
	}
	canDeliver := func(kind string) bool {
		supported := tools.Supported()
		if supported && tools.InFlight() == 0 {
			if since, ok := deferredSince[kind]; ok {
				gate.report(kind, DeliveryBoundary, time.Since(since), 0)
				delete(deferredSince, kind)
			}
			return true
		}
		since, ok := deferredSince[kind]
		if !ok {
			since = time.Now()
			deferredSince[kind] = since
		}
		if wait := gate.maxWait; !supported && wait > 0 && time.Since(since) >= wait {
			gate.report(kind, DeliveryInterrupted, time.Since(since), 0)
			delete(deferredSince, kind)
			return true
		}
		return false
	}
	// queueSteering sends steering as a native queued prompt: the agent
	// finishes its current work, then answers it in the same session.
	queueSteering := func(messages []runstore.SteeringMessage) error {
		if len(messages) == 0 {
			return nil
		}
		if err := steering.dispatch(messages); err != nil {
			return fmt.Errorf("record steering dispatch for worker %s: %w", p.Name, err)
		}
		if journal != nil {
			journal.noteExternalResume()
		}
		sourceOffset := normalizedBoundary(normalizer)
		send(steeringPrompt(messages))
		gate.report(DeliverySteering, DeliveryBoundary, 0, tools.InFlight())
		streamSteered = true
		if steering.opts.OnMessage != nil {
			for _, message := range messages {
				steering.opts.OnMessage(message, sourceOffset)
			}
		}
		return nil
	}
	poll := func(readInput bool) (attempt, bool) {
		if steering != nil {
			_ = steering.activateIfReady(tracker.idValue())
			if readInput {
				steering.poll()
			}
			if steering.errValue() != nil {
				_ = cancelAndDrain()
				return finishAttempt(spec, rawLines.String(), stderr.String(), nil, true, false, false, false, nil, steering.errValue()), true
			}
			if steering.hasPending() && canDeliver(DeliverySteering) {
				if p.Harness == profile.HarnessAntigravity || p.Harness == profile.HarnessKimiCode {
					if err := cancelAndDrain(); err != nil {
						return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, false, false, false, nil, nil), true
					}
					return finishAttempt(spec, rawLines.String(), stderr.String(), nil, true, false, false, true, steering.take(), nil), true
				}
				if err := queueSteering(steering.take()); err != nil {
					_ = cancelAndDrain()
					return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, false, false, false, nil, err), true
				}
			}
		}
		if journal != nil {
			journal.poll()
			if journal.reminderDue() && tracker.idValue() != "" && canDeliver(DeliveryJournal) {
				if err := cancelAndDrain(); err != nil {
					return finishAttempt(spec, rawLines.String(), stderr.String(), err, true, false, false, false, nil, nil), true
				}
				return attempt{started: true, stdout: rawLines.String(), stderr: stderr.String(), journalInterrupted: true}, true
			}
		}
		return attempt{}, false
	}
	for {
		var inputWake <-chan struct{}
		if sourceDoorbell != nil && steering.active {
			inputWake = sourceDoorbell.Wake()
		}
		select {
		case st := <-promptDone:
			inFlight--
			settled[st.seq] = st
			if inFlight > 0 {
				continue // replies may arrive out of order; wait for them all
			}
			s.settled = true
			st = settled[submitted] // the newest prompt owns the turn's answer
			reportDeferred(DeliveryTurnEnd)
			if st.err != nil {
				if s.conn.Done() {
					// Replace this session's dead child on the next attach.
					_ = s.host.Close()
				}
				return finishAttempt(spec, rawLines.String(), stderr.String(), st.err, true, ctx.Err() != nil, journalInterrupted, false, nil, nil)
			}
			finalStderr := s.finishStderr()
			if finalStderr != "" {
				stderr.line(finalStderr)
			}
			runErr := acpStopReasonError(p.Harness, st.res.StopReason)
			if runErr == nil && s.antigravity {
				runErr = antigravityACPPromptError(s.session, finalStderr)
			}
			at := finishAttempt(spec, rawLines.String(), stderr.String(), runErr, true, ctx.Err() != nil, journalInterrupted, false, nil, nil)
			// The final reply names its prompt; that prompt's answer is the
			// turn's, whatever older queued prompts streamed around it.
			at.output = accumulator.Take(st.res.Usage().PromptID)
			at.streamSteered = streamSteered
			return at
		case <-boundary:
			if result, ok := poll(inputWake == nil); ok {
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
		case <-startupC:
			if state.hasStartupActivity() {
				continue
			}
			_ = cancelAndDrain()
			at := finishAttempt(spec, rawLines.String(), stderr.String(), fmt.Errorf("no harness event within %s of startup", startupWait), true, false, false, false, nil, nil)
			at.startupStalled = true
			return at
		case <-ctx.Done():
			if execution.KillOnCancel {
				if local, ok := s.host.(sessionhost.LocalExtension); ok {
					local.KillGroup()
				}
				_ = s.host.Close()
			} else {
				_ = cancelAndDrain()
			}
			return finishAttempt(spec, rawLines.String(), stderr.String(), nil, true, true, false, false, nil, nil)
		}
	}
}

// acpStopReasonError turns a prompt stopReason into a turn error.
func acpStopReasonError(harness, stopReason string) error {
	switch stopReason {
	case "end_turn", "":
		return nil
	case "cancelled":
		return fmt.Errorf("%s turn stopped: cancelled", harness)
	case "max_tokens", "max_turn_requests":
		return fmt.Errorf("%s turn stopped: %s", harness, stopReason)
	case "refusal":
		return fmt.Errorf("%s refused the turn", harness)
	case "rate_limit":
		return fmt.Errorf("%s usage limit reached (rate_limit); try again later", harness)
	case "error":
		return fmt.Errorf("%s ended the turn with an error", harness)
	default:
		return fmt.Errorf("%s turn stopped: %s", harness, stopReason)
	}
}

// boundedLines keeps the raw notification stream for error reporting.
type boundedLines struct {
	mu     sync.Mutex
	lines  []string
	nbytes int
}

func (b *boundedLines) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	b.nbytes += len(line)
	for b.nbytes > 1<<20 && len(b.lines) > 1 {
		b.nbytes -= len(b.lines[0])
		b.lines = b.lines[1:]
	}
}

func (b *boundedLines) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

// acpStderrBuffer collects agent stderr with a bound.
type acpStderrBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *acpStderrBuffer) line(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.Len() < 1<<20 {
		s.b.WriteString(text)
	}
}

func (s *acpStderrBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
