package harness

import (
	"fmt"
	"strings"

	"dyna-agent/internal/runstore"
)

// SteeringSource is what a steering supervisor drains. Run workers use the
// run-owned agent queue (workerSteering); subagent sessions bring their turn gate.
type SteeringSource interface {
	// Activate opens the source to submissions. It must be idempotent.
	Activate() error
	// Read consumes accepted messages after offset. With final set, an empty
	// source is deactivated atomically with respect to submissions.
	Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error)
	// Deactivate closes the source.
	Deactivate()
}

// steeringWake is a source doorbell. A session wait signals either ready
// input or settlement of its outstanding claim; Read consumes that result.
type steeringWake interface {
	Wake() <-chan struct{}
}

type workerSteering struct {
	runID   string
	agentID int
}

func (m workerSteering) Activate() error { return runstore.ActivateAgentSteering(m.runID, m.agentID) }
func (m workerSteering) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	return runstore.ReadAgentSteeringFrom(m.runID, m.agentID, offset, final)
}
func (m workerSteering) Deactivate() { _ = runstore.DeactivateAgentSteering(m.runID, m.agentID) }

type steeringSupervisor struct {
	opts    SteeringOptions
	source  SteeringSource
	offset  int64
	active  bool
	pending []runstore.SteeringMessage
	err     error
	// hook delivers steering through the harness's post-tool hook (nil when
	// the harness has none).
	hook *steerHook
}

func (s *steeringSupervisor) hookValue() *steerHook {
	if s == nil {
		return nil
	}
	return s.hook
}

func newSteeringSupervisor(opts SteeringOptions, resumable bool) *steeringSupervisor {
	if !resumable {
		return nil
	}
	source := opts.Source
	if source == nil {
		if opts.RunID == "" || opts.AgentID <= 0 {
			return nil
		}
		source = workerSteering{runID: opts.RunID, agentID: opts.AgentID}
	}
	return &steeringSupervisor{opts: opts, source: source}
}

func (s *steeringSupervisor) activateIfReady(sessionID string) error {
	if s == nil || s.active || sessionID == "" {
		return nil
	}
	if err := s.source.Activate(); err != nil {
		s.err = err
		return err
	}
	s.active = true
	return nil
}

func (s *steeringSupervisor) poll() {
	if s == nil || !s.active || s.err != nil {
		return
	}
	messages, next, err := s.source.Read(s.offset, false)
	if err != nil {
		s.err = err
		return
	}
	s.offset = next
	s.pending = append(s.pending, messages...)
}

func (s *steeringSupervisor) hasPending() bool {
	return s != nil && len(s.pending) > 0
}

func (s *steeringSupervisor) take() []runstore.SteeringMessage {
	if s == nil || len(s.pending) == 0 {
		return nil
	}
	messages := append([]runstore.SteeringMessage(nil), s.pending...)
	s.pending = nil
	return messages
}

func (s *steeringSupervisor) dispatch(messages []runstore.SteeringMessage) error {
	if s == nil || s.opts.OnDispatch == nil {
		return nil
	}
	for _, message := range messages {
		if err := s.opts.OnDispatch(message); err != nil {
			return err
		}
	}
	return nil
}

func (s *steeringSupervisor) errValue() error {
	if s == nil {
		return nil
	}
	return s.err
}

func (s *steeringSupervisor) finish() ([]runstore.SteeringMessage, error) {
	if s == nil || !s.active {
		return nil, s.errValue()
	}
	if s.err != nil {
		return nil, s.err
	}
	messages, next, err := s.source.Read(s.offset, true)
	if err != nil {
		s.err = err
		return nil, err
	}
	s.offset = next
	if len(messages) == 0 {
		s.active = false
	}
	s.pending = append(s.pending, messages...)
	return s.take(), nil
}

func (s *steeringSupervisor) close() {
	if s == nil || !s.active {
		return
	}
	s.source.Deactivate()
	s.active = false
}

func steeringPrompt(messages []runstore.SteeringMessage) string {
	var b strings.Builder
	b.WriteString("[DYNA STEERING]\nA human or parent model sent the following steering to this exact active worker session. Apply it to the original task, preserve completed work, and continue from the current state. The original final-response contract still applies.\n")
	for i, message := range messages {
		if len(messages) > 1 {
			fmt.Fprintf(&b, "\n%d. %s", i+1, message.Message)
		} else {
			b.WriteString("\n" + message.Message)
		}
	}
	return b.String()
}
