package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"dyna-agent/internal/runstore"
)

var ErrReplayMismatch = errors.New("workflow replay does not match the recorded calls")

type replayIdentity struct {
	kind     runstore.WorkflowCallKind
	identity string
	ordinal  int
}

// ReplayBarrier prevents new effects until every recorded call has matched.
type ReplayBarrier struct {
	mu       sync.Mutex
	pending  map[replayIdentity]runstore.WorkflowManifestCall
	seen     map[replayIdentity]bool
	order    []replayIdentity
	deferred []replayIdentity
	cursor   int
	failure  error
}

func NewReplayBarrier(manifest runstore.WorkflowManifest) (*ReplayBarrier, error) {
	if !manifest.Complete || manifest.UnsafeReason != "" {
		return nil, fmt.Errorf("%w: incomplete or unsafe manifest", ErrReplayMismatch)
	}
	b := &ReplayBarrier{pending: make(map[replayIdentity]runstore.WorkflowManifestCall), seen: make(map[replayIdentity]bool)}
	for _, call := range manifest.Calls {
		key := replayIdentity{call.Kind, call.Identity, call.Ordinal}
		if _, exists := b.pending[key]; exists || call.Result == nil {
			return nil, fmt.Errorf("%w: ambiguous or incomplete call", ErrReplayMismatch)
		}
		b.pending[key] = call
		b.order = append(b.order, key)
	}
	return b, nil
}

// Match returns a saved call, or allows a new call only after replay completes.
func (b *ReplayBarrier) Match(kind runstore.WorkflowCallKind, identity string, ordinal int) (runstore.WorkflowManifestCall, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return runstore.WorkflowManifestCall{}, false, b.failure
	}
	key := replayIdentity{kind, identity, ordinal}
	call, matched := b.pending[key]
	if matched && kind == runstore.WorkflowCallPost {
		delete(b.pending, key)
		b.seen[key] = true
		b.deferred = append(b.deferred, key)
		return call, true, nil
	}
	if matched {
		b.deferred = append(b.deferred, key)
	}
	for _, observed := range b.deferred {
		if b.cursor >= len(b.order) || b.order[b.cursor] != observed {
			b.failure = fmt.Errorf("%w: calls occurred in a different order", ErrReplayMismatch)
			return runstore.WorkflowManifestCall{}, false, b.failure
		}
		b.cursor++
	}
	b.deferred = nil
	if matched {
		delete(b.pending, key)
		b.seen[key] = true
		return call, true, nil
	}
	if b.seen[key] || len(b.pending) > 0 {
		b.failure = fmt.Errorf("%w: unexpected %s call", ErrReplayMismatch, kind)
		return runstore.WorkflowManifestCall{}, false, b.failure
	}
	return runstore.WorkflowManifestCall{}, false, nil
}

func (b *ReplayBarrier) Finish() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	for _, observed := range b.deferred {
		if b.cursor >= len(b.order) || b.order[b.cursor] != observed {
			return fmt.Errorf("%w: calls occurred in a different order", ErrReplayMismatch)
		}
		b.cursor++
	}
	b.deferred = nil
	if len(b.pending) != 0 || b.cursor != len(b.order) {
		return fmt.Errorf("%w: %d calls were not replayed", ErrReplayMismatch, len(b.pending))
	}
	return nil
}

func (b *ReplayBarrier) fail(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure == nil {
		b.failure = fmt.Errorf("%w: %v", ErrReplayMismatch, err)
	}
	return b.failure
}

// replayCall copies a matched result into the new run's manifest before returning it.
func (e *engine) replayCall(kind runstore.WorkflowCallKind, identity string, ordinal int) (bool, any, error) {
	call, matched, err := e.opts.Replay.Match(kind, identity, ordinal)
	if err != nil || !matched {
		return false, nil, err
	}
	var value any
	if err := json.Unmarshal(call.Result, &value); err != nil {
		return true, nil, e.opts.Replay.fail(fmt.Errorf("decode recorded %s result: %w", kind, err))
	}
	var callErr error
	if call.Error != "" {
		callErr = errors.New(call.Error)
	}
	if e.opts.RecordManifest {
		if err := runstore.RegisterWorkflowCall(e.ctx, e.runID(), kind, identity, ordinal); err != nil {
			e.manifestWriteFailed()
			return true, nil, e.opts.Replay.fail(fmt.Errorf("record replay intent: %w", err))
		}
		if err := runstore.CompleteWorkflowCall(context.WithoutCancel(e.ctx), e.runID(), kind, identity, ordinal, call.ExternalID, value, callErr); err != nil {
			e.manifestWriteFailed()
			return true, nil, e.opts.Replay.fail(fmt.Errorf("record replay result: %w", err))
		}
	}
	return true, value, callErr
}

// checkReplayCwd rejects working directories not covered by the input capture.
func (e *engine) checkReplayCwd(cwd string) error {
	if !e.opts.CheckReplaySources {
		return nil
	}
	workDir, err := filepath.Abs(e.opts.WorkDir)
	if err != nil {
		return err
	}
	if cwd == workDir {
		return nil
	}
	reason := fmt.Errorf("working directory %q was not captured before the run", cwd)
	if e.opts.Replay != nil {
		return e.opts.Replay.fail(reason)
	}
	if err := runstore.MarkWorkflowManifestUnsafe(e.ctx, e.runID(), reason.Error()); err != nil {
		e.manifestWriteFailed()
		return fmt.Errorf("mark working directory unsafe: %w", err)
	}
	return nil
}
