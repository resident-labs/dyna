package harness

import (
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// streamTurn drives the shared persistent-input turn protocol: steering
// delivery, journal nudges, replay-acknowledgment handling and the decision
// that a terminal result seals the turn. It is the one implementation of
// that path (rules u1/u3); the runner that owns the child calls into it and
// supplies how a sealed turn ends. Accounting flows back through nudges and
// steered, which the runner copies onto its attempt.
type streamTurn struct {
	p        profile.Profile
	stream   *streamInput
	steering *steeringSupervisor
	journal  *journalSupervisor
	tracker  *sessionTracker
	gate     *deliveryGate
	tools    *workeroutput.ToolTracker
	paused   func() bool

	nudges  int  // journal reminders written this turn
	steered bool // steering written with its dispatch recorded
}

// streamFinish ends the turn at a sealed terminal result. messages stay
// owned by this Dyna turn when the child cannot take more input, so the
// next attempt resumes them exactly.
type streamFinish func(terminal *streamTerminal, messages []runstore.SteeringMessage, journalPending, continuation bool) attempt

// steeringQueued reports pending steering a later wake may deliver.
func (t *streamTurn) steeringQueued() bool { return t.steering != nil && t.steering.hasPending() }

// pending returns input this turn still owns: stream messages the worker has
// not covered, then queued steering. It must be read before the attach ends,
// so the next attempt can resume it.
func (t *streamTurn) pending() (messages []runstore.SteeringMessage, journalPending bool) {
	messages, journalPending = t.stream.pending()
	if t.steering != nil {
		messages = append(messages, t.steering.take()...)
	}
	return messages, journalPending
}

// writeSteering hands pending steering to the worker. Claude takes it in at
// its next tool boundary, so dyna does not gate or interrupt. A failed write
// requeues the messages, so they resume in the next turn instead of being
// lost.
func (t *streamTurn) writeSteering() error {
	if t.paused() {
		return nil
	}
	if t.steering == nil || !t.steering.hasPending() || !t.stream.open() {
		return nil
	}
	messages := t.steering.take()
	if err := t.steering.dispatch(messages); err != nil {
		return fmt.Errorf("record steering dispatch for worker %s: %w", t.p.Name, err)
	}
	if t.journal != nil {
		t.journal.noteExternalResume()
	}
	if t.paused() {
		t.steering.pending = append(messages, t.steering.pending...)
		return nil
	}
	write := func() error {
		if !t.stream.write(streamItemSteering, steeringPrompt(messages), messages, t.tools.InFlight()) {
			return errors.New("worker input closed")
		}
		return nil
	}
	var deliveryErr error
	if t.steering.opts.DeliveryGate != nil {
		deliveryErr = t.steering.opts.DeliveryGate(write)
	} else {
		deliveryErr = write()
	}
	if deliveryErr != nil {
		t.steering.pending = append(messages, t.steering.pending...)
	} else {
		t.steered = true
	}
	return nil
}

// poll advances steering and journal delivery. readInput drains the steering
// source when the wake came from it. An error fails the turn; the runner
// owns how the child stops then.
func (t *streamTurn) poll(readInput bool) error {
	if t.steering != nil {
		if err := t.steering.activateIfReady(t.tracker.idValue()); err != nil {
			return err
		}
		if readInput {
			t.steering.poll()
		}
		if err := t.steering.errValue(); err != nil {
			return err
		}
		if err := t.writeSteering(); err != nil {
			return err
		}
	}
	if t.paused() {
		return nil
	}
	if t.journal != nil {
		t.journal.poll()
		if !t.journal.reminderDue() {
			return nil
		}
		if t.stream.write(streamItemJournal, journalNudgePrompt, nil, t.tools.InFlight()) {
			t.journal.noteStreamNudge()
			t.nudges++
		}
	}
	return nil
}

// handle drains stream notices: it reports input the worker took in and
// asks the runner to finish when a terminal result seals the turn. finished
// reports whether the returned attempt ends the turn; a non-nil error ends
// it too, with the runner building the attempt.
func (t *streamTurn) handle(finish streamFinish) (attempt, bool, error) {
	for _, n := range t.stream.drain() {
		if c := n.consumed; c != nil {
			switch c.kind {
			case streamItemPrompt, streamItemSteering:
				if t.steering != nil && t.steering.opts.OnMessage != nil {
					for _, message := range c.messages {
						t.steering.opts.OnMessage(message, c.sourceOffset)
					}
				}
				if c.kind == streamItemSteering && c.inFlight > 0 {
					t.gate.report(DeliverySteering, DeliveryBoundary, time.Since(c.written), t.tools.InFlight())
				}
			case streamItemJournal:
				if c.inFlight > 0 {
					t.gate.report(DeliveryJournal, DeliveryBoundary, time.Since(c.written), t.tools.InFlight())
				}
			}
		}
		terminal := n.terminal
		if !t.stream.canSeal(terminal) {
			continue
		}
		if t.steering != nil {
			if err := t.steering.activateIfReady(t.tracker.idValue()); err != nil {
				return attempt{}, true, err
			}
			// The final empty read must happen before the turn seals, so a
			// late message is not dropped.
			if !t.steering.hasPending() {
				messages, err := t.steering.finish()
				if err != nil {
					return attempt{}, true, err
				}
				t.steering.pending = append(t.steering.pending, messages...)
			}
			if t.steering.hasPending() {
				if err := t.writeSteering(); err != nil {
					return attempt{}, true, err
				}
				if t.steering.hasPending() { // write failed: resume in the next turn
					return finish(terminal, t.steering.take(), false, true), true, nil
				}
				continue // newly written input needs its own result
			}
		}
		return finish(terminal, nil, false, false), true, nil
	}
	return attempt{}, false, nil
}

// stalled reports whether the acknowledgment bound armed: only input the
// worker has not acknowledged, with no tool in flight and a quiet stdout,
// ever arms it. A quiet tool never times out.
func (t *streamTurn) stalled(limit time.Duration) bool {
	return t.stream.awaitingAcknowledgment() && t.tools.InFlight() == 0 && t.stream.quietFor() >= limit
}

// streamPollInterval picks how often poll runs between wakes: file-backed
// workflow queues poll, session input doorbells, and the journal reminder
// interval can shorten it.
func streamPollInterval(steering *steeringSupervisor, journal *journalSupervisor, doorbell bool) time.Duration {
	interval := workerSteeringPollInterval
	if doorbell {
		interval = steeringDeliveryInterval
	}
	if steering != nil && steering.opts.pollEvery > 0 {
		interval = steering.opts.pollEvery
	}
	if journal != nil && journal.pollInterval() < interval {
		interval = journal.pollInterval()
	}
	return interval
}
