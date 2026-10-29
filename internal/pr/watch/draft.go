package watch

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"fmt"
	"strings"
	"sync"
	"time"
)

func (s *Service) setHold(key string, hold *state.Hold) error {
	return s.Store.updateRecordOrAll(key, func(*State) bool { return false }, func(st *State) error {
		st.Records[key].Hold = hold
		return nil
	})
}

// draftLocks holds one lock per PR key. A package variable keeps Service copyable.
var draftLocks = struct {
	sync.Mutex
	byKey map[string]*sync.Mutex
}{byKey: map[string]*sync.Mutex{}}

// lockDraft serializes the hold and release calls for one PR and returns its unlock.
func lockDraft(key string) (unlock func()) {
	draftLocks.Lock()
	mu := draftLocks.byKey[key]
	if mu == nil {
		mu = &sync.Mutex{}
		draftLocks.byKey[key] = mu
	}
	draftLocks.Unlock()
	mu.Lock()
	return mu.Unlock
}

// clearHold removes the hold only while it is still held. A hold that a concurrent
// call set after the release started stays.
func (s *Service) clearHold(key string, held *state.Hold) error {
	return s.Store.updateRecordOrAll(key, func(*State) bool { return false }, func(st *State) error {
		cur := st.Records[key].Hold
		if cur != nil && cur.Reason == held.Reason && cur.By == held.By && cur.At.Equal(held.At) {
			st.Records[key].Hold = nil
		}
		return nil
	})
}

// SetDraft holds a PR in Dyna's state and as a GitHub draft. Release clears both.
func (s *Service) SetDraft(ctx context.Context, key string, draft bool, reason string) error {
	reason = strings.TrimSpace(reason)
	if draft && reason == "" {
		return fmt.Errorf("a hold reason is required")
	}
	// The lock covers the read, the state write and the GitHub call, so a hold and a
	// release for one PR run one at a time and their GitHub calls cannot cross.
	unlock := lockDraft(key)
	defer unlock()
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	if !r.Watching || r.State != state.PROpen || changeKind(r) != "github" {
		return fmt.Errorf("PR is not open and tracked")
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("a PR hold or release needs the room store")
	}
	if _, err := s.Notify.Rooms.Resolve(r.RoomID); err != nil {
		return err
	}
	args := []string{"pr", "ready"}
	text := fmt.Sprintf("%s released. GitHub draft status no longer holds this PR.", changeLabel(r))
	if draft {
		args = append(args, "--undo")
		text = fmt.Sprintf("%s held as a GitHub draft. Reason: %s", changeLabel(r), Sanitize(reason))
	}
	args = append(args, "--", r.URL)
	// The hold is saved before the GitHub call, so a crash or failure still leaves the PR held.
	// Release keeps the hold until GitHub succeeds. The engine's own gh pr ready passes the
	// guard through a context value that only engine code sets.
	if draft {
		actor := authority.Of(ctx)
		hold := &state.Hold{Reason: reason, By: actor.Session, At: time.Now().UTC()}
		if err := s.setHold(key, hold); err != nil {
			return err
		}
	} else {
		ctx = ghclient.WithHoldRelease(ctx, r.URL)
	}
	if _, err := s.github(r.RoomID).command(ctx, "", false, args...); err != nil {
		return err
	}
	if !draft && r.Hold != nil {
		if err := s.clearHold(key, r.Hold); err != nil {
			return err
		}
	}
	if _, err := s.deliver(ctx, r, Notice{Text: text}); err != nil {
		return fmt.Errorf("GitHub draft status changed, but the room post failed: %w", err)
	}
	return nil
}
