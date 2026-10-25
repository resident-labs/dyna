package rolesession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

type InspectFunc func(string) (subagent.Info, subagent.Turn, error)

type LookupResult struct {
	Info    subagent.Info
	Turn    subagent.Turn
	Usable  bool
	Adopted bool
}

// Lookup first checks the saved ID, then the visible named session. named must
// resolve roomName + ":" + role. A missing or closed session needs recreation.
// The caller persists an adopted ID before dispatching work.
func Lookup(id, roomID, role string, inspect InspectFunc, named func() (subagent.Info, error)) LookupResult {
	var out LookupResult
	valid := func(info subagent.Info) bool {
		return info.RoomID == roomID && info.Role == role && info.Status != subagent.StatusClosed
	}
	if id != "" {
		info, turn, err := inspect(id)
		if err == nil && valid(info) {
			return LookupResult{Info: info, Turn: turn, Usable: true}
		}
	}
	if named != nil {
		found, err := named()
		if err == nil && valid(found) {
			info, turn, err := inspect(found.ID)
			if err == nil && valid(info) {
				return LookupResult{Info: info, Turn: turn, Usable: true, Adopted: true}
			}
		}
	}
	return out
}

// NeedsRecreation excludes provider failures, which require profile failover.
func NeedsRecreation(info subagent.Info, turn subagent.Turn) bool {
	if turn.Status == subagent.TurnError && ProviderFailure(turn) {
		return false
	}
	return turn.Status == subagent.TurnError ||
		(turn.Done() && (info.Status == subagent.StatusIdle || info.Status == subagent.StatusDormant) && (!info.Resumable || info.HarnessSession == ""))
}

func Transferring(info subagent.Info) bool { return info.Transfer != nil && info.Transfer.Active() }
func Busy(info subagent.Info) bool {
	return Transferring(info) || info.Status == subagent.StatusRunning || info.Status == subagent.StatusQueued
}

type Decision string

const (
	Ready            Decision = "ready"
	WaitBusy         Decision = "busy"
	Rotate           Decision = "rotate"
	WaitTurn         Decision = "already-assigned"
	DeadlineExceeded Decision = "deadline-exceeded"
)

type Policy struct {
	Profile        string
	Assignments    int
	MaxAssignments int
	MaxInputTokens int64
	// AssignedTurn is the receiving turn, not the turn observed before sending.
	// An idle follow-up normally receives info.Turns + 1. Keep this guard until
	// a newer turn begins. Reset it when replacing the session.
	Assigned     bool
	AssignedTurn int
	Now          time.Time
	Deadline     time.Time
}

// DecideAssignment checks the hard deadline even while busy. A zero limit or
// deadline disables that check. Rotation is a separate action, never an
// assignment in the same dispatch. Callers persist the assignment turn under
// Lock before sending work. No turn can accept two assignments with this gate.
func DecideAssignment(info subagent.Info, turn subagent.Turn, p Policy) Decision {
	if !p.Deadline.IsZero() && !p.Now.Before(p.Deadline) {
		return DeadlineExceeded
	}
	if Busy(info) {
		return WaitBusy
	}
	if p.Assigned && info.Turns <= p.AssignedTurn {
		return WaitTurn
	}
	if (p.MaxAssignments > 0 && p.Assignments >= p.MaxAssignments) ||
		(p.MaxInputTokens > 0 && turn.Usage.InputTokens >= p.MaxInputTokens) || info.Profile != p.Profile {
		return Rotate
	}
	return Ready
}

// AssignmentContext bounds the actual session action as well as the decision.
// A zero deadline leaves cancellation with the parent context.
func AssignmentContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

// Lock serializes actions for a role in a room, across processes. Never hold
// a role's state-store lock while starting, closing, or transferring sessions.
func Lock(root, role, roomID string) (*os.File, error) {
	sum := sha256.Sum256([]byte(roomID))
	return LockPath(filepath.Join(root, role+"-"+hex.EncodeToString(sum[:])[:16]+".lock"))
}

// LockWait waits for the holder until ctx ends, preserving the role's lock path.
func LockWait(ctx context.Context, root, role, roomID string) (func(), error) {
	sum := sha256.Sum256([]byte(roomID))
	path := filepath.Join(root, role+"-"+hex.EncodeToString(sum[:])[:16]+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return store.LockContext(ctx, path)
}

// LockPath allows existing roles to preserve their on-disk lock location.
func LockPath(path string) (*os.File, error) {
	return lockPath(path)
}

func lockPath(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func Unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
