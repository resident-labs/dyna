package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
	"github.com/google/uuid"
)

func runOpenCodeTurn(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, outputPath, normalizedOutputPath string, output *workeroutput.Declaration, liveLine func(string), gate *deliveryGate, state *runState, execution ExecutionOptions) (out attempt) {
	if steering != nil && steering.opts.BeforeStart != nil {
		if err := steering.opts.BeforeStart(ctx, spec.prompt); err != nil {
			return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
		}
	}
	sess, err := openCodeSessionProcessFor(ctx, p, cwd, spec, execution)
	if err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	defer sess.detach()
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
	if output == nil {
		output = p.EffectiveOutput()
	}
	normalizer := workeroutput.NewNormalizer(output, normalizedOutput)
	tools := workeroutput.NewToolTracker(output)
	state.beginProcess()
	stdout := newObservedBuffer(func(line string) {
		if liveLine != nil {
			liveLine(line)
		}
		record := workeroutput.DecodeLine(output, line)
		tracker.observeRecord(record, line)
		state.observeRecord(record)
		_ = normalizer.WriteRecord(record)
		tools.ObserveSignals(record.Signals)
	}, liveOutput)
	started, _ := json.Marshal(map[string]any{"type": "session", "sessionID": sess.session})
	_, _ = io.WriteString(stdout, string(started)+"\n")
	notifications := openCodeTurnNotifications{session: sess.session, pending: map[string]bool{}}
	emptyLimit := emptyStepLimit
	if execution.emptyStepLimit > 0 {
		emptyLimit = execution.emptyStepLimit
	}
	drain := func() {
		for _, u := range sess.drain() {
			if err := sess.answerHeadless(ctx, u, p.SafeMode); err != nil {
				notifications.err = err
				notifications.done = true
				_ = sess.host.Close()
			}
			for _, line := range notifications.observe(u) {
				_, _ = io.WriteString(stdout, line+"\n")
			}
		}
		if state.stepsStalled(emptyLimit) && !notifications.done {
			notifications.err = fmt.Errorf("worker made no progress after %d empty steps", emptyLimit)
			sess.kill()
		}
		sess.idle = notifications.done
	}
	streamNudges := 0
	streamSteered := false
	finish := func(runErr error, contextDone bool) attempt {
		drain()
		if journal != nil {
			journal.poll()
		}
		var messages []runstore.SteeringMessage
		if steering != nil {
			_ = steering.activateIfReady(tracker.idValue())
			steering.poll()
			messages = steering.take()
		}
		at := finishAttempt(spec, stdout.String(), nativeSessionStderr(sess.host), runErr, true, contextDone, false, false, messages, steering.errValue())
		at.streamNudges, at.streamSteered = streamNudges, streamSteered
		return at
	}
	submit := func(text string) error {
		id := "msg_dyna_" + uuid.NewString()
		notifications.pending[id] = true
		if err := sess.prompt(ctx, text, "steer", id); err != nil {
			delete(notifications.pending, id)
			return err
		}
		return nil
	}
	var startBoundary *int64
	send := func() error {
		startBoundary = normalizedBoundary(normalizer)
		return submit(spec.prompt)
	}
	if steering != nil && steering.opts.StartGate != nil {
		err = steering.opts.StartGate(ctx, spec.prompt, send)
	} else {
		err = send()
	}
	if err != nil {
		// A failed request can leave inference running.
		// Stop that process; a later attempt resumes its persisted thread.
		sess.kill()
		return finish(err, ctx.Err() != nil)
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
	if spec.journalReminderReason != "" && journal != nil {
		journal.markNudged(true, spec.journalReminderReason)
	}
	// A successful interrupt response only acknowledges the request. The terminal
	// notification proves tools have stopped and the process can serve another turn.
	interrupt := func() {
		drain()
		if notifications.done {
			return
		}
		if execution.KillOnCancel {
			sess.kill()
			drain()
			return
		}
		interruptCtx, cancel := context.WithTimeout(context.Background(), openCodeCommandTimeout)
		defer cancel()
		// Abort must also retract inputs still waiting in the native inbox.
		for id := range notifications.pending {
			_ = sess.request(interruptCtx, "DELETE", sess.path("/inbox/"+id), nil, nil)
		}
		var reply struct {
			Interrupted bool `json:"interrupted"`
		}
		if err := sess.request(interruptCtx, "POST", sess.path("/interrupt"), nil, &reply); err != nil {

			sess.kill()
			drain()
			return
		}
		if !reply.Interrupted {
			notifications.done = true
			sess.idle = true
			return
		}
		for !notifications.done {
			drain()
			if notifications.done {
				return
			}
			select {
			case <-sess.activity:
			case <-sess.exited:
				drain()
				return
			case <-interruptCtx.Done():
				sess.kill()
				drain()
				return
			}
		}
	}
	paused := func() bool { return steering != nil && steering.opts.Paused != nil && steering.opts.Paused() }
	steer := func(text string) error {
		return submit(text)
	}
	poll := func(readInput bool) error {
		drain()
		if notifications.done || paused() {
			return nil
		}
		if steering != nil {
			_ = steering.activateIfReady(tracker.idValue())
			if readInput {
				steering.poll()
			}
			if err := steering.errValue(); err != nil {
				return err
			}
			if steering.hasPending() {
				messages := steering.take()
				if err := steering.dispatch(messages); err != nil {
					steering.pending = append(messages, steering.pending...)
					return err
				}
				var sourceOffset *int64
				send := func() error {
					sourceOffset = normalizedBoundary(normalizer)
					return steer(steeringPrompt(messages))
				}
				var err error
				if steering.opts.DeliveryGate != nil {
					err = steering.opts.DeliveryGate(send)
				} else {
					err = send()
				}
				if err != nil {
					steering.pending = append(messages, steering.pending...)
					drain()
					// Drain a terminal event that raced this rejected steer.
					if notifications.done {
						return nil
					}
					return err
				}
				if journal != nil {
					journal.noteExternalResume()
				}
				if steering.opts.OnMessage != nil {
					for _, message := range messages {
						steering.opts.OnMessage(message, sourceOffset)
					}
				}
				streamSteered = true
				gate.report(DeliverySteering, DeliveryBoundary, 0, tools.InFlight())
			}
		}
		if journal != nil {
			journal.poll()
			if journal.reminderDue() {
				drain()
				if notifications.done {
					return nil
				}
				if err := steer(journalNudgePrompt); err != nil {
					journal.markNudged(false, JournalNudgeIdle)
				} else {
					journal.noteStreamNudge()
					streamNudges++
				}
			}
		}
		return nil
	}
	var sourceDoorbell steeringWake
	if steering != nil {
		sourceDoorbell, _ = steering.source.(steeringWake)
	}
	var tick <-chan time.Time
	if steering != nil || journal != nil {
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
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	startupWait := startupLimit
	if execution.startupLimit > 0 {
		startupWait = execution.startupLimit
	}
	timer := time.NewTimer(startupWait)
	defer timer.Stop()
	startupC := timer.C
	if execution.startupFired != nil {
		startupC = execution.startupFired
	}
	for {
		drain()
		if notifications.done {
			return finish(notifications.err, false)
		}
		var inputWake <-chan struct{}
		if sourceDoorbell != nil {
			_ = steering.activateIfReady(tracker.idValue())
			if steering.active {
				inputWake = sourceDoorbell.Wake()
			}
		}
		select {
		case <-sess.activity:
			if err := sess.exitError(); err != nil {
				sess.kill()
				return finish(err, false)
			}
			if err := poll(false); err != nil {
				interrupt()
				return finish(err, false)
			}
		case <-inputWake:
			if err := poll(true); err != nil {
				interrupt()
				return finish(err, false)
			}
		case <-tick:
			if err := poll(sourceDoorbell == nil || inputWake == nil); err != nil {
				interrupt()
				return finish(err, false)
			}
		case <-sess.exited:
			drain()
			if notifications.done {
				return finish(notifications.err, false)
			}
			return finish(sess.exitError(), ctx.Err() != nil)
		case <-ctx.Done():
			interrupt()
			return finish(ctx.Err(), true)
		case <-startupC:
			if state.hasStartupActivity() {
				continue
			}
			interrupt()
			at := finish(fmt.Errorf("no harness event within %s of startup", startupWait), false)
			at.startupStalled = true
			return at
		}
	}
}
