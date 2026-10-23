package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// claudeInterruptGrace is how long a canceled stream-json turn waits for the
// interrupt control_response before the child is abandoned. The CLI aborts
// locally, so this is not a model round trip. Tests may shorten it.
var claudeInterruptGrace = 2 * time.Second

// claudeHostedInput writes stream-json lines to the shared session. The host
// adds the LF that ends each frame, so the line's trailing LF is removed first.
type claudeHostedInput struct{ sess sessionhost.Session }

func (w claudeHostedInput) Write(b []byte) (int, error) {
	if err := w.sess.Write(bytes.TrimSuffix(b, []byte("\n"))); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close is a no-op. Ending a turn must not stop the child the host owns.
func (w claudeHostedInput) Close() error { return nil }

// claudeLaunchFingerprint identifies the Claude child without its session
// selector. A fresh --session-id and a later --resume name the same child.
func claudeLaunchFingerprint(argv, env []string, dir string) string {
	return sessionLaunchFingerprint(argv[0], env, claudeWithoutSelector(argv)) + "\x00" + dir
}

// claudeWithoutSelector drops only the exact --session-id or --resume pair.
func claudeWithoutSelector(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if (argv[i] == "--session-id" || argv[i] == "--resume") && i+1 < len(argv) {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

type claudeHostedFrame struct {
	line []byte
	err  error
}

// runClaudeTurn runs one stream-json turn on the shared session host. Input
// delivery, nudges and sealing go through streamTurn, the one shared
// persistent-input path; this runner owns only the child: one session
// process per key, detached idle at a covered result so the next turn
// reattaches to it, abandoned when its turn cannot settle.
func runClaudeTurn(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, liveLine func(string), gate *deliveryGate, state *runState, execution ExecutionOptions) (out attempt) {
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, spec.prompt); err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	launch, err := nativeSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	// Turns that differ only in the session selector share one child.
	launch.Fingerprint = claudeLaunchFingerprint(launch.Argv, launch.Env, launch.Dir)
	hosted, err := nativeSessionHost.Session(ctx, launch)
	if err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	state.beginProcess()

	// The reader goroutine is the only caller of Next. end stops it and joins it
	// before the attach ends, so a later attach never receives this turn's frames.
	transport := newNativeSessionTransport(hosted)
	frames := make(chan claudeHostedFrame)
	stopReader := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, err := transport.Read()
			select {
			case frames <- claudeHostedFrame{line: line, err: err}:
			case <-stopReader:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	stream := newStreamInput(claudeHostedInput{sess: hosted})
	ended := false
	// end detaches the attach once. DetachAbandoned makes the host kill the child.
	end := func(reason sessionhost.DetachReason) {
		if ended {
			return
		}
		ended = true
		stream.close()
		close(stopReader)
		_ = transport.Close()
		<-readerDone
		hosted.Detach(reason)
	}
	defer end(sessionhost.DetachAbandoned)

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
	stream.captureBoundary = func() *int64 { return normalizedBoundary(normalizer) }
	tools := workeroutput.NewToolTracker(output)
	emptyLimit := emptyStepLimit
	if execution.emptyStepLimit > 0 {
		emptyLimit = execution.emptyStepLimit
	}
	stallLimit := streamStallLimit
	if execution.streamStallLimit > 0 {
		stallLimit = execution.streamStallLimit
	}

	// Stdout lines are observed only on this goroutine, through the select loop.
	// The flags below therefore need no lock.
	stdoutEnd := 0
	boundary, sessionReady := false, false
	stdout := newObservedBuffer(func(line string) {
		stdoutEnd += len(line) + 1
		if liveLine != nil {
			liveLine(line)
		}
		record := workeroutput.DecodeLine(output, line)
		tracker.observeRecord(record, line)
		if tracker.idValue() != "" {
			sessionReady = true
		}
		state.observeRecord(record)
		stream.observe(line, stdoutEnd, state.currentUsage())
		_ = normalizer.WriteRecord(record)
		if tools.ObserveSignals(record.Signals) {
			boundary = true
		}
	}, liveOutput)

	turn := &streamTurn{
		p: p, stream: stream, steering: steering, journal: journal,
		tracker: tracker, gate: gate, tools: tools,
		paused: func() bool { return steering != nil && steering.opts.Paused != nil && steering.opts.Paused() },
	}
	defer func() {
		out.streamNudges, out.streamSteered = turn.nudges, turn.steered
	}()

	// fail stops the attach and returns the input that was not yet covered. The
	// pending list is read before the attach ends, so the next attempt can resume it.
	fail := func(runErr error, contextDone bool, steeringErr error) attempt {
		messages, journalPending := turn.pending()
		stderr := nativeSessionStderr(hosted)
		end(sessionhost.DetachAbandoned)
		return finishAttempt(spec, stdout.String(), stderr, runErr, true, contextDone, journalPending, len(messages) > 0, messages, steeringErr)
	}
	// finishTurn ends the attempt at a result. The child stays only when the
	// result succeeded and nothing is queued for a later turn.
	finishTurn := func(terminal *streamTerminal, messages []runstore.SteeringMessage, journalPending, continuation bool) attempt {
		state.sealUsage(terminal.usage)
		runErr := workeroutput.ClaudeResultError(terminal.line)
		stderr := nativeSessionStderr(hosted)
		if runErr == nil && len(messages) == 0 && !journalPending && !continuation {
			end(sessionhost.DetachIdle)
		} else {
			end(sessionhost.DetachAbandoned)
		}
		a := finishAttempt(spec, stdout.String()[:terminal.stdoutEnd], stderr, runErr, true, ctx.Err() != nil, journalPending, continuation, messages, nil)
		a.stdout = stdout.String()
		return a
	}
	writePrompt := func() error {
		// set_model is written before every prompt, so the same child serves
		// the profile's model on each turn.
		if model := strings.TrimSpace(p.Model); model != "" {
			if !stream.writeControl(workeroutput.ClaudeControlSetModel(nextClaudeControlID(), model)) {
				return errors.New("worker input closed before set_model")
			}
		}
		if !stream.write(streamItemPrompt, spec.prompt, spec.steering, 0) {
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
		return fail(promptErr, ctx.Err() != nil, nil)
	}
	if steering != nil {
		if steering.opts.OnStart != nil {
			steering.opts.OnStart()
		}
	}
	if spec.journalReminderReason != "" && journal != nil {
		journal.markNudged(true, spec.journalReminderReason)
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
	stallTicker := time.NewTicker(min(time.Second, stallLimit))
	defer stallTicker.Stop()

	var sourceDoorbell steeringWake
	if steering != nil {
		sourceDoorbell, _ = steering.source.(steeringWake)
	}
	var tick <-chan time.Time
	if steering != nil || journal != nil {
		ticker := time.NewTicker(streamPollInterval(steering, journal, sourceDoorbell != nil))
		defer ticker.Stop()
		tick = ticker.C
	}

	// Once an interrupt begins, nothing new is written to this turn. Steering
	// and journal input wait for the next turn.
	interrupting, ackOK, sessionPolled := false, false, false
	interruptID := ""
	var before *streamTerminal
	var graceC <-chan time.Time
	ctxDone := ctx.Done()
	for {
		var inputWake <-chan struct{}
		if sourceDoorbell != nil && steering != nil && !interrupting {
			if err := steering.activateIfReady(tracker.idValue()); err != nil {
				return fail(err, ctx.Err() != nil, err)
			}
			if steering.active {
				inputWake = sourceDoorbell.Wake()
			}
		}
		wakeNil := inputWake == nil
		select {
		case f := <-frames:
			if f.err != nil {
				return fail(f.err, ctx.Err() != nil, nil)
			}
			_, _ = io.WriteString(stdout, string(f.line)+"\n")
			if state.stepsStalled(emptyLimit) {
				a := fail(fmt.Errorf("no output after %d empty steps in a row", emptyLimit), ctx.Err() != nil, nil)
				a.noProgress = true
				return a
			}
			if interrupting {
				if id, success, ok := workeroutput.ClaudeControlResponse(string(f.line)); ok && id == interruptID && success {
					ackOK = true
				}
				if t := stream.lastTerminal(); t != nil && t != before {
					// The result ends the interrupted turn. Keep the child only when the
					// CLI acknowledged the interrupt and nothing else is queued.
					messages, journalPending := turn.pending()
					keep := ackOK && stream.canSeal(t) && len(messages) == 0 && !journalPending && !turn.steeringQueued()
					stderr := nativeSessionStderr(hosted)
					if keep {
						end(sessionhost.DetachIdle)
					} else {
						end(sessionhost.DetachAbandoned)
					}
					a := finishAttempt(spec, stdout.String()[:t.stdoutEnd], stderr, ctx.Err(), true, true, journalPending, false, messages, nil)
					a.stdout = stdout.String()
					return a
				}
				continue
			}
			if sessionReady && !sessionPolled {
				sessionPolled = true
				if err := turn.poll(false); err != nil {
					return fail(err, ctx.Err() != nil, err)
				}
			}
			if boundary {
				boundary = false
				if err := turn.poll(wakeNil); err != nil {
					return fail(err, ctx.Err() != nil, err)
				}
			}
			if a, finished, err := turn.handle(finishTurn); err != nil {
				return fail(err, ctx.Err() != nil, err)
			} else if finished {
				return a
			}
		case <-ctxDone:
			ctxDone = nil
			if execution.KillOnCancel {
				killNativeSession(hosted)
				return fail(ctx.Err(), true, nil)
			}
			interrupting = true
			before = stream.lastTerminal()
			interruptID = nextClaudeControlID()
			if !stream.writeControl(workeroutput.ClaudeControlInterrupt(interruptID)) {
				return fail(ctx.Err(), true, nil)
			}
			graceC = time.After(claudeInterruptGrace)
		case <-graceC:
			return fail(ctx.Err(), true, nil)
		case <-inputWake:
			if err := turn.poll(true); err != nil {
				return fail(err, ctx.Err() != nil, err)
			}
		case <-tick:
			if !interrupting {
				if err := turn.poll(wakeNil); err != nil {
					return fail(err, ctx.Err() != nil, err)
				}
			}
		case <-stallTicker.C:
			if !interrupting && turn.stalled(stallLimit) {
				messages, journalPending := turn.pending()
				var runErr error
				if len(messages) == 0 && !journalPending && !turn.steeringQueued() {
					runErr = errors.New("Claude produced no result covering acknowledged input")
				}
				return fail(runErr, ctx.Err() != nil, nil)
			}
		case <-startupC:
			if state.hasStartupActivity() {
				// The worker started; a slow first thought is not a hang.
				continue
			}
			a := fail(fmt.Errorf("no harness event within %s of startup", startupWait), ctx.Err() != nil, nil)
			a.startupStalled = true
			return a
		}
	}
}
