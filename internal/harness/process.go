package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/workeroutput"
)

func runOnce(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, resumable bool, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, liveLine func(string), gate *deliveryGate, state *runState, execution ExecutionOptions) (out attempt) {
	if spec.acpPrompt != "" {
		return runAcpAttempt(ctx, p, cwd, spec, tracker, journal, steering, resumable, outputPath, normalizedOutputPath, output, liveLine, gate, state, execution)
	}
	if err := validateWorkingDirectory(cwd); err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	if spec.rpc {
		return runPiTurn(ctx, p, cwd, spec, tracker, journal, steering, outputPath, normalizedOutputPath, output, liveLine, gate, state, execution)
	}
	if execution.ProcessRunner != nil {
		return runInProcess(ctx, spec, tracker, journal, steering, outputPath, normalizedOutputPath, output, state, execution.ProcessRunner)
	}
	if spec.codex {
		return runCodexTurn(ctx, p, cwd, spec, tracker, journal, steering, outputPath, normalizedOutputPath, output, liveLine, gate, state, execution)
	}
	// Persistent-input bookkeeping rides on every returned attempt.
	streamNudges := 0
	streamSteered := false
	defer func() {
		out.streamNudges, out.streamSteered = streamNudges, streamSteered
	}()
	argv := spec.argv
	hook := steering.hookValue()
	if hook == nil {
		var err error
		hook, err = newSteerHook(p)
		if err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
		if hook != nil {
			defer hook.cleanup()
		}
	}
	cmd, err := harnessCommand(ctx, p, cwd, argv, execution)
	if err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	if hook != nil {
		cmd.Args, err = hook.argv(cmd.Args, cmd.Env)
		if err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
		hook.failed = false
	}
	var stream *streamInput
	if spec.streamInput {
		w, err := cmd.StdinPipe()
		if err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
		stream = newStreamInput(w)
		defer stream.close()
	} else if spec.stdinPrompt {
		cmd.Stdin = strings.NewReader(spec.prompt)
	}
	state.beginProcess()
	var liveOutput *os.File
	if outputPath != "" {
		liveOutput, _ = os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0)
	}
	if liveOutput != nil {
		defer liveOutput.Close()
	}
	var normalizedOutput *os.File
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
	// A model can loop on empty steps and never fail. After emptyStepLimit of
	// them in a row, observeLine signals noProgress and the loop below stops
	// the worker so the fallback chain can move on.
	emptyLimit := emptyStepLimit
	if execution.emptyStepLimit > 0 {
		emptyLimit = execution.emptyStepLimit
	}
	noProgress := make(chan struct{}, 1)
	observeLine := func(line string, stdoutEnd int) {
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
		if stream != nil {
			stream.observe(line, stdoutEnd, state.currentUsage())
		}
		_ = normalizer.WriteRecord(record)
		if tools.ObserveSignals(record.Signals) {
			select {
			case boundary <- struct{}{}:
			default:
			}
		}
	}
	stdoutEnd := 0
	onLine := func(line string) {
		stdoutEnd += len(line) + 1
		if liveLine != nil {
			liveLine(line)
		}
		observeLine(line, stdoutEnd)
	}
	stdout := newObservedBuffer(onLine, liveOutput)
	defer stdout.FlushPending(func(line string) {
		record := workeroutput.DecodeLine(output, line)
		state.observeRecord(record)
		_ = normalizer.WriteRecord(record)
	})
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	waitCommand, release, stage, err := startGuarded(ctx, p, cmd, spec.prompt, steering, execution)
	defer release()
	switch stage {
	case startBlocked:
		return finishAttempt(spec, stdout.String(), stderr.String(), err, false, true, false, false, nil, nil)
	case startUnguarded:
		return finishAttempt(spec, stdout.String(), stderr.String(), err, false, false, false, false, nil, nil)
	case startFailed:
		if cwdErr := validateWorkingDirectory(cwd); cwdErr != nil {
			err = cwdErr
		}
		return finishAttempt(spec, stdout.String(), stderr.String(), err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	if !spec.streamInput && steering != nil && steering.opts.OnStart != nil {
		steering.opts.OnStart()
	}
	if !spec.streamInput && steering != nil && steering.opts.OnMessage != nil {
		for _, message := range spec.steering {
			steering.opts.OnMessage(message)
		}
	}
	if spec.journalReminderReason != "" && journal != nil {
		journal.markNudged(true, spec.journalReminderReason)
	}
	var streamSig <-chan struct{}
	var streamTick <-chan time.Time
	stallLimit := streamStallLimit
	if execution.streamStallLimit > 0 {
		stallLimit = execution.streamStallLimit
	}
	if stream != nil {
		t := time.NewTicker(min(time.Second, stallLimit))
		defer t.Stop()
		streamTick = t.C
		streamSig = stream.signal
		if steering != nil && steering.opts.BeforeStart != nil {
			if err := steering.opts.BeforeStart(ctx, spec.prompt); err != nil {
				_ = signalProcessGroup(cmd, syscall.SIGTERM)
				_ = waitCommand()
				return finishAttempt(spec, stdout.String(), stderr.String(), err, true, true, false, false, nil, nil)
			}
		}
		writePrompt := func() error {
			if !stream.write(streamItemPrompt, spec.prompt, nil, 0) {
				return errors.New("worker input closed before prompt delivery")
			}
			return nil
		}
		var promptErr error
		if steering != nil && steering.opts.StartGate != nil {
			promptErr = steering.opts.StartGate(ctx, spec.prompt, writePrompt)
		} else {
			promptErr = writePrompt()
		}
		if promptErr != nil {
			_ = signalProcessGroup(cmd, syscall.SIGTERM)
			_ = waitCommand()
			return finishAttempt(spec, stdout.String(), stderr.String(), promptErr, true, ctx.Err() != nil, false, false, nil, nil)
		}
		if steering != nil {
			if steering.opts.OnStart != nil {
				steering.opts.OnStart()
			}
			if steering.opts.OnMessage != nil {
				for _, msg := range spec.steering {
					steering.opts.OnMessage(msg)
				}
			}
		}
	}
	done := make(chan error, 1)
	go func() { done <- waitCommand() }()

	var sourceDoorbell steeringWake
	if steering != nil {
		sourceDoorbell, _ = steering.source.(steeringWake)
	}
	var tick <-chan time.Time
	var ticker *time.Ticker
	if journal != nil || steering != nil {
		// File-backed workflow queues still need their own poll. Session
		// input uses a doorbell; this timer checks only pending delivery and
		// journal reminders, without reading or claiming idle input.
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

	// The startup limit stops a worker that produces no harness event at all,
	// so a hung startup fails with its stderr and exit detail instead of
	// hanging. Once any thinking, tool, usage or text event arrives the worker
	// has started, and the timer is disarmed for the rest of the turn.
	startupWait := startupLimit
	if execution.startupLimit > 0 {
		startupWait = execution.startupLimit
	}
	startupTimer := time.NewTimer(startupWait)
	startupC := startupTimer.C
	defer startupTimer.Stop()
	if execution.startupFired != nil {
		startupC = execution.startupFired
	}

	// A pending steering message or due journal reminder waits for the next tool
	// completion, or for the turn to end, however long the tool runs: an
	// interrupt there would kill the tool call. Only a stream without tool
	// boundaries is interrupted, after steering.maxToolWait, because dyna cannot
	// see whether a tool is running. deferredSince records when each kind first
	// had to wait.
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
			gate.report(kind, DeliveryUnsupported, time.Since(since), 0)
			delete(deferredSince, kind)
			return true
		}
		return false
	}
	// poll is shared by the ticker and tool-completion wakeups. It returns a
	// finished attempt when a delivery interrupted the worker.
	// writeSteering hands pending steering to a persistent-input worker; Claude
	// takes it in at its next tool boundary, so dyna does not gate or interrupt.
	paused := func() bool { return steering != nil && steering.opts.Paused != nil && steering.opts.Paused() }
	writeSteering := func() error {
		if paused() {
			return nil
		}
		if stream == nil || steering == nil || !steering.hasPending() || !stream.open() {
			return nil
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
		write := func() error {
			if !stream.write(streamItemSteering, steeringPrompt(messages), messages, tools.InFlight()) {
				return errors.New("worker input closed")
			}
			return nil
		}
		var deliveryErr error
		if steering.opts.DeliveryGate != nil {
			deliveryErr = steering.opts.DeliveryGate(write)
		} else {
			deliveryErr = write()
		}
		if deliveryErr != nil {
			steering.pending = append(messages, steering.pending...)
		} else {
			streamSteered = true
		}
		return nil
	}
	// deliverHooked records messages the post-tool hook handed to the worker
	// inside its running turn.
	deliverHooked := func(messages []runstore.SteeringMessage, since time.Time) error {
		if len(messages) == 0 {
			return nil
		}
		if err := steering.dispatch(messages); err != nil {
			return fmt.Errorf("record steering dispatch for worker %s: %w", p.Name, err)
		}
		if steering.opts.OnMessage != nil {
			for _, message := range messages {
				steering.opts.OnMessage(message)
			}
		}
		if journal != nil {
			journal.noteExternalResume()
		}
		gate.report(DeliverySteering, DeliveryBoundary, time.Since(since), tools.InFlight())
		streamSteered = true
		return nil
	}
	// hookSteering offers pending steering to the worker's post-tool hook and
	// notices when the hook took it. It never stops the worker.
	hookSteering := func() error {
		since := hook.offeredAt
		if err := deliverHooked(hook.taken(), since); err != nil {
			return err
		}
		if hook.stale() {
			back, delivered := hook.withdraw()
			if err := deliverHooked(delivered, since); err != nil {
				return err
			}
			if len(back) > 0 {
				// The hook did not run: use the interrupt path from here on.
				hook.failed = true
				steering.pending = append(back, steering.pending...)
				return nil
			}
		}
		if !steering.hasPending() || tracker.idValue() == "" {
			return nil
		}
		back, delivered := hook.withdraw()
		if err := deliverHooked(delivered, since); err != nil {
			return err
		}
		if len(back) == 0 {
			since = time.Now()
		}
		messages := append(back, steering.take()...)
		if paused() {
			steering.pending = append(messages, steering.pending...)
			return nil
		}
		offer := func() error { return hook.offer(tracker.idValue(), messages, since) }
		var offerErr error
		if steering.opts.DeliveryGate != nil {
			offerErr = steering.opts.DeliveryGate(offer)
		} else {
			offerErr = offer()
		}
		if offerErr != nil {
			if !paused() {
				hook.failed = true
			}
			steering.pending = append(messages, steering.pending...)
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
				runErr := stopCanceledProcess(cmd, done)
				return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, steering.errValue()), true
			}
			if stream != nil {
				if err := writeSteering(); err != nil {
					runErr := stopCanceledProcess(cmd, done)
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err), true
				}
			} else if hook.usable() {
				if err := hookSteering(); err != nil {
					runErr := stopCanceledProcess(cmd, done)
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err), true
				}
			} else if !paused() && steering.hasPending() && resumable && tracker.idValue() != "" && canDeliver(DeliverySteering) {
				runErr, interrupted := interruptForContinuation(ctx, cmd, done)
				messages := steering.take()
				if ctx.Err() != nil {
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil), true
				}
				return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, interrupted, messages, steering.errValue()), true
			}
		}
		if paused() {
			return attempt{}, false
		}
		if journal != nil {
			journal.poll()
			if !journal.reminderDue() {
				return attempt{}, false
			}
			if stream != nil {
				if stream.write(streamItemJournal, journalNudgePrompt, nil, tools.InFlight()) {
					journal.noteStreamNudge()
					streamNudges++
				}
				return attempt{}, false
			}
			if resumable {
				if tracker.idValue() == "" || !canDeliver(DeliveryJournal) {
					return attempt{}, false
				}
				runErr, interrupted := interruptForContinuation(ctx, cmd, done)
				journal.poll()
				if ctx.Err() != nil {
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil), true
				}
				return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, interrupted, false, nil, nil), true
			}
			journal.markNudged(false, JournalNudgeIdle)
		}
		return attempt{}, false
	}

	// A sealed protocol result commits this turn. Process retirement is a
	// separate, synchronous phase; native background tasks cannot extend it or
	// replace its output. Durable delegated work belongs to
	// subagents, which have their own lifecycle and completion delivery.
	finishStream := func(terminal *streamTerminal, messages []runstore.SteeringMessage, journalPending bool, continuation bool) attempt {
		state.sealUsage(terminal.usage)
		stream.close()
		retireErr := retireClaudeProcess(cmd, done)
		runErr := workeroutput.ClaudeResultError(terminal.line)
		if runErr == nil {
			runErr = retireErr
		}
		logical := stdout.String()[:terminal.stdoutEnd]
		a := finishAttempt(spec, logical, stderr.String(), runErr, true, ctx.Err() != nil, journalPending, continuation, messages, nil)
		// Keep the full transport trace, including anything printed during
		// retirement. Only parsing and usage use the sealed terminal boundary.
		a.stdout = stdout.String()
		return a
	}
	handleStream := func() (attempt, bool) {
		for _, n := range stream.drain() {
			if c := n.consumed; c != nil {
				switch c.kind {
				case streamItemSteering:
					if steering != nil && steering.opts.OnMessage != nil {
						for _, message := range c.messages {
							steering.opts.OnMessage(message)
						}
					}
					if c.inFlight > 0 {
						gate.report(DeliverySteering, DeliveryBoundary, time.Since(c.written), tools.InFlight())
					}
				case streamItemJournal:
					if c.inFlight > 0 {
						gate.report(DeliveryJournal, DeliveryBoundary, time.Since(c.written), tools.InFlight())
					}
				}
			}
			terminal := n.terminal
			if !stream.canSeal(terminal) {
				continue
			}
			if steering != nil {
				if err := steering.activateIfReady(tracker.idValue()); err != nil {
					runErr := stopCanceledProcess(cmd, done)
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err), true
				}
				// Already-read input must be written before an empty final read
				// can seal the source. finish() itself makes the final empty
				// read and gate closure atomic with concurrent senders.
				if !steering.hasPending() {
					messages, err := steering.finish()
					if err != nil {
						runErr := stopCanceledProcess(cmd, done)
						return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err), true
					}
					steering.pending = append(steering.pending, messages...)
				}
				if steering.hasPending() {
					if err := writeSteering(); err != nil {
						runErr := stopCanceledProcess(cmd, done)
						return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err), true
					}
					if steering.hasPending() { // write failed: resume after retiring this process
						return finishStream(terminal, steering.take(), false, true), true
					}
					continue // newly written input needs its own result
				}
			}
			return finishStream(terminal, nil, false, false), true
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
		case runErr := <-done:
			var streamMessages []runstore.SteeringMessage
			streamJournal := false
			if stream != nil {
				// EOF can terminate a valid final JSON record without a newline.
				// Wait has joined the reader, so observing that tail is ordered
				// after every complete line and cannot race stdoutEnd.
				stdout.FlushPending(func(line string) {
					stdoutEnd += len(line)
					observeLine(line, stdoutEnd)
				})
				// Wait includes stdout reader completion. A result already in
				// the notice queue has the same authority whichever select wins.
				done <- runErr
				if a, finished := handleStream(); finished {
					return a
				}
				if retireErr := retireClaudeProcess(cmd, done); runErr == nil {
					runErr = retireErr
				}
				streamMessages, streamJournal = stream.pending()
				if stream.awaitingResult() && len(streamMessages) == 0 && !streamJournal {
					runErr = fmt.Errorf("Claude exited without a result covering acknowledged input")
				}
			}
			if journal != nil {
				journal.poll()
			}
			if ctx.Err() != nil {
				return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
			}
			messages := streamMessages
			if steering != nil {
				_ = steering.activateIfReady(tracker.idValue())
				steering.poll()
				if ctx.Err() != nil {
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
				}
				var back []runstore.SteeringMessage
				if hook != nil {
					// A message the hook did not take becomes the continuation.
					since := hook.offeredAt
					var delivered []runstore.SteeringMessage
					back, delivered = hook.withdraw()
					if err := deliverHooked(delivered, since); err != nil {
						return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, err)
					}
					if len(back) > 0 {
						gate.report(DeliverySteering, DeliveryTurnEnd, time.Since(since), tools.InFlight())
					}
				}
				messages = append(messages, back...)
				messages = append(messages, steering.take()...)
			}
			reportDeferred(DeliveryTurnEnd)
			return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, streamJournal, len(streamMessages) > 0, messages, steering.errValue())
		case <-ctx.Done():
			if execution.KillOnCancel {
				_ = signalProcessGroup(cmd, syscall.SIGKILL)
			}
			runErr := stopCanceledProcess(cmd, done)
			if journal != nil {
				journal.poll()
			}
			return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
		case <-noProgress:
			runErr := stopCanceledProcess(cmd, done)
			a := finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, nil)
			a.noProgress = true
			a.runErr = fmt.Errorf("no output after %d empty steps in a row", emptyLimit)
			return a
		case <-startupC:
			if state.hasStartupActivity() {
				// The worker started; a slow first thought is not a hang.
				continue
			}
			runErr := stopCanceledProcess(cmd, done)
			a := finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, nil)
			a.startupStalled = true
			a.runErr = fmt.Errorf("no harness event within %s of startup", startupWait)
			return a
		case <-streamSig:
			if a, finished := handleStream(); finished {
				return a
			}
		case <-streamTick:
			// Only a protocol terminal with uncovered input arms this bound.
			// Quiet tools on other harnesses are unaffected. The state lives
			// in streamInput so stale/empty notice drains cannot clear it.
			if stream.awaitingAcknowledgment() && tools.InFlight() == 0 && stream.quietFor() >= stallLimit {
				messages, journalPending := stream.pending()
				if steering != nil {
					messages = append(messages, steering.take()...)
				}
				terminal := stream.lastTerminal()
				a := finishStream(terminal, messages, journalPending, len(messages) > 0)
				// An acknowledged input with no subsequent result still needs
				// exact-session recovery, rather than returning an older answer.
				if len(messages) == 0 && !journalPending {
					a.runErr = fmt.Errorf("Claude produced no result covering acknowledged input")
				}
				return a
			}
		case <-boundary:
			hook.noteBoundary()
			if result, ok := poll(inputWake == nil); ok {
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

// Kinds and outcomes of a tool-boundary delivery decision.

// harnessCommand builds the process for argv with the profile's launch
// environment and private executable. Turns and compact commands share it.
func harnessCommand(ctx context.Context, p profile.Profile, cwd string, argv []string, execution ExecutionOptions) (*exec.Cmd, error) {
	// buildInvocation already rejected invalid compaction and instructions.
	_, compactEnv, _ := profile.CompactNative(p)
	launchEnv := withWorkingDirectoryEnv(mergeEnv(mergeEnv(os.Environ(), p.Env), compactEnv), cwd)
	// An explicit empty scratch override means setup failed. Remove the variable
	// so a worker cannot accidentally use its parent's or profile's scratch path.
	if value, ok := p.Env[scratch.Env]; ok && value == "" {
		launchEnv = slices.DeleteFunc(launchEnv, func(pair string) bool { return strings.HasPrefix(pair, scratch.Env+"=") })
	}
	if !execution.SkipComputer {
		launchEnv = computerEnvironment(ctx, launchEnv)
	}
	// Private generations can include Dyna's server with either shell policy.
	// Forward its environment only when the generated server actually exists.
	if p.RuntimeExecutable != "" {
		var err error
		switch p.Harness {
		case profile.HarnessCodex:
			argv, err = harnessruntime.DynaShellCodexArgs(argv, launchEnv)
		case profile.HarnessMuse:
			argv, err = harnessruntime.DynaShellMuseArgs(argv, launchEnv)
		}
		if err != nil {
			return nil, err
		}
	}

	executable := argv[0]
	if p.DynaManaged() {
		if p.RuntimeExecutable == "" {
			return nil, fmt.Errorf("private runtime executable is missing")
		}
		executable = p.RuntimeExecutable
	}
	cmd := exec.CommandContext(ctx, executable, argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = launchEnv
	// Escaped descendants can retain output pipes after the harness exits.
	// Bound pipe cleanup for every execution so completion and cancellation finish.
	cmd.WaitDelay = time.Second
	return cmd, nil
}

// runCompaction runs a harness's compact command on a session through the
// same guarded start as a turn, including the room gates. Stdout goes to a
// line writer, not a pipe Dyna reads, so a descendant that keeps stdout open
// cannot block cleanup past WaitDelay. Stdin stays open until spec.finished
// sees the result, because Pi's RPC mode exits at end of input. A failure
// stops the remaining commands. An admitted API request can already have
// changed context.
func runCompaction(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, steering *steeringSupervisor, execution ExecutionOptions, admit func(context.Context) error) (completed int, err error) {
	for current := &spec; current != nil; current = current.after {
		if err := runCompactCommand(ctx, p, cwd, *current, steering, execution, admit); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func runCompactCommand(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, steering *steeringSupervisor, execution ExecutionOptions, admit func(context.Context) error) error {
	if spec.rpc {
		return runPiCompact(ctx, p, cwd, spec, steering, execution, admit)
	}
	if err := validateWorkingDirectory(cwd); err != nil {
		return err
	}
	if err := admit(ctx); err != nil {
		return err
	}
	cmd, err := harnessCommand(ctx, p, cwd, spec.argv, execution)
	if err != nil {
		return err
	}
	var compactStderr bytes.Buffer
	cmd.Stderr = &compactStderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	stdout := newObservedBuffer(func(line string) {
		if spec.finished == nil {
			return
		}
		if done, err := spec.finished(line); done {
			select {
			case result <- err:
				_ = stdin.Close()
			default:
			}
		}
	}, nil)
	cmd.Stdout = stdout
	wait, release, _, err := startGuarded(ctx, p, cmd, "", steering, execution)
	defer release()
	if err != nil {
		return err
	}
	_, _ = io.WriteString(stdin, spec.prompt)
	exited := make(chan error, 1)
	go func() { exited <- wait() }()
	select {
	case err := <-result:
		// The harness exits once its input closes; stop a straggler.
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = signalProcessGroup(cmd, syscall.SIGKILL)
			<-exited
		}
		return err
	case werr := <-exited:
		if spec.finished == nil {
			if werr != nil {
				return fmt.Errorf("compact command failed: %w: %s", werr, strings.TrimSpace(compactStderr.String()))
			}
			if spec.checkOutput != nil {
				return spec.checkOutput(stdout.String())
			}
			return nil
		}
		select {
		case err := <-result:
			return err
		default:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the compact command ended without a result: %v", werr)
	}
}

// Stages at which startGuarded can stop.
const (
	startDone      = iota
	startBlocked   // BeforeStart refused, for example while the room is paused
	startUnguarded // the private-runtime process guard could not be set up
	startFailed    // the gate or the process start failed
)

// startGuarded starts cmd the way every harness process starts: in its own
// process group, after the room gates, under the private-runtime process
// guard bound to the runtime lease. Turns and Dyna's compact command share it.
// prompt is what the gates hold; "" holds none. It returns the wait function
// and a release func the caller must defer.
func startGuarded(ctx context.Context, p profile.Profile, cmd *exec.Cmd, prompt string, steering *steeringSupervisor, execution ExecutionOptions) (func() error, func(), int, error) {
	release := func() {}
	// Own process group so a timeout/cancel kills the whole worker tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if execution.KillOnCancel {
			return signalProcessGroup(cmd, syscall.SIGKILL)
		}
		return signalProcessGroup(cmd, syscall.SIGTERM)
	}
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, prompt); err != nil {
			return nil, release, startBlocked, err
		}
	}
	childenv.Clean(cmd)
	startCommand := cmd.Start
	waitCommand := cmd.Wait
	if p.DynaManaged() {
		controller, guardErr := processguard.Wrap(cmd)
		if guardErr != nil {
			return nil, release, startUnguarded, guardErr
		}
		if execution.KillOnCancel {
			if err := controller.SetCancelSignal(syscall.SIGKILL); err != nil {
				return nil, release, startUnguarded, err
			}
		}
		guardedCommands.Store(cmd, controller)
		release = func() { guardedCommands.Delete(cmd) }
		startCommand = func() error {
			return controller.Start(func(ref processguard.Reference) error {
				raw, err := json.Marshal(ref)
				if err != nil {
					return err
				}
				if p.RuntimeBindProcess == nil {
					return errors.New("Private runtime process protection is missing.")
				}
				return p.RuntimeBindProcess(raw)
			})
		}
		waitCommand = controller.Wait
	}
	startProcess := startCommand
	if steering != nil && steering.opts.StartGate != nil {
		startProcess = func() error { return steering.opts.StartGate(ctx, prompt, startCommand) }
	}
	if err := startProcess(); err != nil {
		return nil, release, startFailed, err
	}
	if execution.Started != nil {
		execution.Started(cmd.Process.Pid)
	}
	return waitCommand, release, startDone, nil
}
