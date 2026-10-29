package approvals

import (
	"context"
	"database/sql"
	"errors"
)

// ExecuteFunc runs the exact payload of one approved request.
type ExecuteFunc func(context.Context, Request) (Result, error)

// Register binds one kind to its engine-side executor. Registration happens
// during engine construction, before approval calls are served.
func (s *Service) Register(kind Kind, execute ExecuteFunc) {
	s.execMu.Lock()
	s.executors[kind] = execute
	s.execMu.Unlock()
}

// SetLifetime lets decision-triggered execution stop with the engine.
func (s *Service) SetLifetime(ctx context.Context) {
	s.execMu.Lock()
	s.runCtx = ctx
	s.execMu.Unlock()
}

func (s *Service) trigger(r Request) {
	execute, ctx, ok := s.claim(r)
	if !ok {
		return
	}
	go func() { _ = s.execute(ctx, r, execute) }()
}

func (s *Service) claim(r Request) (ExecuteFunc, context.Context, bool) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	execute := s.executors[r.Kind]
	key := r.Room + "\x00" + r.ID
	if execute == nil || s.running[key] {
		return nil, nil, false
	}
	s.running[key] = true
	return execute, s.runCtx, true
}

// RunApproved recovers requests whose decision committed before a restart.
// A request already running in this process is not started a second time.
func (s *Service) RunApproved(ctx context.Context) error {
	requests, err := s.withState(StateApproved)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range requests {
		execute, _, ok := s.claim(r)
		if !ok {
			continue
		}
		if err := s.execute(ctx, r, execute); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) execute(ctx context.Context, r Request, execute ExecuteFunc) error {
	defer func() {
		s.execMu.Lock()
		delete(s.running, r.Room+"\x00"+r.ID)
		s.execMu.Unlock()
	}()
	result, runErr := execute(ctx, r)
	if ctx.Err() != nil {
		// Engine shutdown leaves the approved row for the next start.
		return nil
	}
	if r.Result != nil && result.Actor == "" {
		result.Actor = r.Result.Actor
	}
	state := StateDone
	if runErr != nil {
		state = StateFailed
		result.Reason = runErr.Error()
		if result.ExitCode == nil {
			code := 1
			result.ExitCode = &code
		}
	} else if result.ExitCode != nil && *result.ExitCode != 0 {
		state = StateFailed
	}
	now := s.now().UTC()
	err := s.write(ctx, func(conn *sql.Conn) error {
		resultJSON, err := writeResult(Request{Result: &result})
		if err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx, "UPDATE approval_requests SET state=?,result=?,updated_at=? WHERE room=? AND id=? AND state='approved'", state, resultJSON, now.UnixNano(), r.Room, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			return ErrNotPending
		}
		return err
	})
	if errors.Is(err, ErrNotPending) {
		return nil
	}
	if err != nil {
		return err
	}
	r.State, r.Result, r.UpdatedAt = state, &result, now
	if err := s.postResult(r, false); err != nil {
		return err
	}
	s.changed(r.Room, r.ID)
	return nil
}
