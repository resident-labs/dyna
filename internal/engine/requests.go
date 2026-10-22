package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/state"
	"dyna-agent/internal/websvc"
)

// applyOnce makes each call that carries a request ID apply once
// (requirement 9). A retry with the same ID, from the same caller, gets the
// first result and writes nothing. A result explicitly marked read-only
// saves no receipt, so a retry can read again.
//
// The first SQL write of the call records the ID in that write's transaction, so a
// write that committed is never applied again, even when the engine stopped
// before the result was stored; that retry gets a null result. A call that
// writes only files or logs records its ID with its result, after it runs.
// Calls with the same ID run one at a time. A call that failed before any
// write committed leaves no record, so its retry runs again.
func applyOnce(calls map[string]websvc.Call, w *state.Writer, logf func(string, ...any)) {
	once := &requestRunner{w: w, logf: logf}
	checks := cachedReplyChecks()
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			id := callspec.RequestOf(ctx)
			if id == "" {
				return call(ctx, args)
			}
			key := requestKey(ctx, id)
			if check := checks[name]; check != nil {
				if err := check(ctx, args); err != nil {
					return nil, err
				}
				key = runnerRequestKey(id)
			}
			return once.run(ctx, key, name, call, args)
		}
	}
}

// Cached replies bypass bindings and actor wrappers. These execution calls
// check their verified caller before reading a caller-independent runner key.
func cachedReplyChecks() map[string]func(context.Context, []json.RawMessage) error {
	checks := map[string]func(context.Context, []json.RawMessage) error{
		// A wait has no receipt or replay identity. Refuse a supplied ID before
		// the shared request wrapper can read or persist a cached response.
		"RunnerWaitInput": func(context.Context, []json.RawMessage) error {
			return errors.New("read-only input waits do not carry request IDs")
		},
		"AdmitNativeAttempt": func(ctx context.Context, args []json.RawMessage) error {
			var in callspec.AccountAdmission
			if len(args) != 1 {
				return errors.New("native admission needs one account admission argument")
			}
			if err := json.Unmarshal(args[0], &in); err != nil {
				return err
			}
			return checkAdmissionCaller(ctx, in)
		},
		"ObserveMuseQuota": func(ctx context.Context, args []json.RawMessage) error {
			var in callspec.MuseQuotaObservation
			if len(args) != 1 {
				return errors.New("Muse quota observation needs one observation argument")
			}
			if err := json.Unmarshal(args[0], &in); err != nil {
				return err
			}
			return checkMuseQuotaCaller(ctx, in)
		},
		"ObserveClaudeUsage": func(ctx context.Context, args []json.RawMessage) error {
			var in callspec.ClaudeUsageObservation
			if len(args) != 1 {
				return errors.New("Claude usage observation needs one observation argument")
			}
			if err := json.Unmarshal(args[0], &in); err != nil {
				return err
			}
			return checkClaudeUsageCaller(ctx, in)
		},
	}
	for name := range runnerCalls() {
		checks[name] = checkRunnerCachedReply
	}
	return checks
}

func checkRunnerCachedReply(ctx context.Context, _ []json.RawMessage) error {
	caller, local := websvc.CallerFromContext(ctx)
	if !local || caller.Kind != websvc.Session || caller.Session == "" {
		return errors.New("only a verified session's own turn runner can read its saved reply")
	}
	return callspec.CheckRunnerRequestID(caller.Session, callspec.RequestOf(ctx))
}

// requestRunner runs calls once per caller-scoped request key.
type requestRunner struct {
	w       *state.Writer
	logf    func(string, ...any)
	running requestLocks
}

func (r *requestRunner) run(ctx context.Context, key, name string, call websvc.Call, args []json.RawMessage) (any, error) {
	unlock, err := r.running.lock(ctx, key)
	if err != nil {
		return nil, err
	}
	defer unlock()
	fingerprint, err := requestFingerprint(name, args)
	if err != nil {
		return nil, err
	}
	ctx = state.ForRequestCall(ctx, key, name, fingerprint)
	if result, done, err := r.w.RequestDone(ctx, key); err != nil {
		return nil, err
	} else if done {
		return json.RawMessage(nullIfEmpty(result)), nil
	}
	res, err := call(ctx, args)
	if errors.Is(err, state.ErrRepeated) {
		result, _, err := r.w.RequestDone(ctx, key)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(nullIfEmpty(result)), nil
	}
	if err != nil {
		return res, err
	}
	if read, ok := res.(interface{ RequestReadOnly() bool }); ok && read.RequestReadOnly() {
		return res, nil
	}
	result, err := json.Marshal(res)
	if err == nil {
		saved, done, lookupErr := r.w.RequestDone(ctx, key)
		if errors.Is(lookupErr, state.ErrRequestConflict) {
			err = lookupErr
		} else if lookupErr != nil || !done || !bytes.Equal(saved, result) {
			// A failed lookup keeps the existing finalization attempt.
			err = r.w.FinishRequest(ctx, key, name, result)
		}
	}
	if err != nil {
		if errors.Is(err, state.ErrRequestConflict) {
			return nil, err
		}
		// A committed mutation receipt remains replayable if saving the
		// response fails after the call applied.
		r.logf("requests: %s %s applied, but its result was not stored: %v", name, key, err)
	}
	return res, nil
}

// requestFingerprint canonicalizes object keys without rounding JSON numbers.
// Nil and empty argument lists both represent a call with no arguments.
func requestFingerprint(method string, args []json.RawMessage) ([]byte, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		if !json.Valid(arg) {
			return nil, fmt.Errorf("%s argument %d is not valid JSON", method, i+1)
		}
		decoder := json.NewDecoder(bytes.NewReader(arg))
		decoder.UseNumber()
		if err := decoder.Decode(&values[i]); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal([]any{method, values})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

// runnerRequestKey is the key of a turn runner's call. It does not depend on
// how the engine resolved the caller, so a spooled line the engine applies
// later (runner_spool.go) has the same key as its live attempt. A runner's
// request ID starts with its verified session. applyOnce checks that prefix
// before reading a saved result; subagent.runnerSession checks it before
// writing, so no caller can take another session's key.
func runnerRequestKey(id string) string { return "runner/" + id }

// requestKey scopes a request ID to its caller, so one caller can neither
// replay another's result nor block another's ID: a local caller by its
// resolved identity, a paired device or Connect session by its own ID.
func requestKey(ctx context.Context, id string) string {
	caller, local := websvc.CallerFromContext(ctx)
	p, _ := websvc.PrincipalFromContext(ctx)
	return callerRequestKey(caller, local, p, id)
}

func callerRequestKey(caller websvc.Caller, local bool, p websvc.Principal, id string) string {
	if local {
		return fmt.Sprintf("local/%s/%s/%s/%v/%s", caller.Kind, caller.Session, caller.Run, caller.Agent, id)
	}
	return fmt.Sprintf("%s/%s/%s", p.Kind, p.ID, id)
}

func nullIfEmpty(result []byte) []byte {
	if len(result) == 0 {
		return []byte("null")
	}
	return result
}

// requestLocks lets calls with the same request ID run one at a time.
type requestLocks struct {
	mu   sync.Mutex
	held map[string]chan struct{}
}

func (l *requestLocks) lock(ctx context.Context, id string) (unlock func(), err error) {
	for {
		l.mu.Lock()
		busy, ok := l.held[id]
		if !ok {
			if l.held == nil {
				l.held = map[string]chan struct{}{}
			}
			free := make(chan struct{})
			l.held[id] = free
			l.mu.Unlock()
			return func() {
				l.mu.Lock()
				delete(l.held, id)
				l.mu.Unlock()
				close(free)
			}, nil
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-busy:
		}
	}
}

// pruneRequests forgets request IDs older than state.RequestKeep, at start
// and then every hour on the engine's timer set (requirement 28).
func pruneRequests(ctx context.Context, t *timers, w *state.Writer) {
	var sweep func()
	sweep = func() {
		cutoff := t.now().Add(-state.RequestKeep)
		var batch func()
		batch = func() {
			if ctx.Err() != nil {
				return
			}
			n, err := w.PruneRequests(ctx, cutoff)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("requests: prune: %v", err)
			}
			if err == nil && n > 0 {
				// Release the writer and hand control back to the timer set
				// between batches. This sweep keeps its original cutoff.
				t.at(timerRequests, t.now(), batch)
			} else {
				// Errors yield to the next normal sweep, never a retry loop.
				t.at(timerRequests, t.now().Add(time.Hour), sweep)
			}
		}
		batch()
	}
	sweep()
}
