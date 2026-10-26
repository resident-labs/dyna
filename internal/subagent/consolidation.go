package subagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/pr/links"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// ConsolidationState stays with the kept session. Source transcripts and task
// directories remain independent archives; consolidation never changes git.
type ConsolidationState struct {
	PausedFor     time.Duration         `json:"pausedFor,omitempty"`
	ID            string                `json:"id"`
	RoomID        string                `json:"roomId"`
	Target        string                `json:"target"`
	Sessions      []string              `json:"sessions"`
	Profile       string                `json:"profile"`
	Model         string                `json:"model"`
	Phase         string                `json:"phase"`
	Requested     time.Time             `json:"requested"`
	Deadline      time.Time             `json:"deadline"`
	Ended         time.Time             `json:"ended,omitempty"`
	Error         string                `json:"error,omitempty"`
	CancelPending bool                  `json:"cancelPending,omitempty"`
	Sources       []ConsolidationSource `json:"sources"`
	Charter       string                `json:"charter,omitempty"`
}
type ConsolidationSource struct {
	PendingIDs  []string       `json:"pendingIds,omitempty"`
	Reservation *TransferState `json:"reservation,omitempty"`
	Session     string         `json:"session"`
	Address     string         `json:"address"`
	Phase       string         `json:"phase"`
	Source      string         `json:"source,omitempty"`
	Error       string         `json:"error,omitempty"`
	Context     string         `json:"context,omitempty"`
	Dir         string         `json:"dir,omitempty"`
	// Inbox uses the original cursor, queue and delivery records. It is never
	// copied onto the target's cursor or claim map.
	Cursor int64 `json:"cursor"`
}
type ConsolidationOptions struct {
	Sessions []string `json:"sessions"`
	Target   string   `json:"target"`
	Profile  string   `json:"profile,omitempty"`
	Model    string   `json:"model,omitempty"`
}

func (c *ConsolidationState) Active() bool {
	return c != nil && c.Phase != "completed" && (c.Phase != "cancelled" || c.CancelPending)
}
func consolidationToken(op, id string) string { return op + "-" + id }

// lockSessions takes metadata locks in a stable order, inside names.lock.
func (s *Store) lockSessions(ids []string, fn func() error) error {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	var take func(int) error
	take = func(i int) error {
		if i == len(ids) {
			return fn()
		}
		return s.withLock(ids[i], func() error { return take(i + 1) })
	}
	return take(0)
}
func (m *Manager) Consolidate(ctx context.Context, opts ConsolidationOptions) (ConsolidationState, error) {
	var out ConsolidationState
	if err := authority.Check("subagent consolidate", authority.Of(ctx), nil); err != nil {
		return out, err
	}
	if len(opts.Sessions) < 2 || len(opts.Sessions) > 16 {
		return out, errors.New("select 2 to 16 sessions")
	}
	target, err := m.store().Resolve(opts.Target)
	if err != nil {
		return out, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, ref := range opts.Sessions {
		info, e := m.store().Resolve(ref)
		if e != nil {
			return out, e
		}
		if seen[info.ID] {
			return out, errors.New("select distinct sessions")
		}
		seen[info.ID] = true
		ids = append(ids, info.ID)
	}
	sort.Strings(ids)
	if !seen[target.ID] {
		return out, errors.New("the kept session must be selected")
	}
	ps, err := m.profiles()
	if err != nil {
		return out, err
	}
	profileName := opts.Profile
	if profileName == "" {
		profileName = target.Profile
	}
	p, ok := ps.Get(profileName)
	if !ok || p.Disabled {
		return out, errors.New("target profile is unavailable")
	}
	if err := checkProfileUse(*p, target.Service); err != nil {
		return out, err
	}
	chosen := *p
	if opts.Model != "" {
		chosen.Model = strings.TrimSpace(opts.Model)
		chosen.Models = nil
	} else if opts.Profile == "" && target.Model != "" {
		chosen.Model = target.Model
		chosen.Models = nil
	}
	if len(chosen.Model) > 256 || strings.ContainsAny(chosen.Model, "\r\n\x00") {
		return out, errors.New("invalid model name")
	}
	if err := chosen.Validate(); err != nil {
		return out, err
	}
	unlock, err := lockNames(m.store())
	if err != nil {
		return out, err
	}
	err = m.store().lockSessions(ids, func() error {
		if m.RoomPaused(target.RoomID) {
			return errRoomPaused
		}
		kept, e := m.store().ReadMeta(target.ID)
		if e != nil {
			return e
		}
		if c := kept.Consolidation; c != nil && reflect.DeepEqual(c.Sessions, ids) && c.Profile == profileName && c.Model == chosen.Model {
			out = *c
			return nil
		}
		now := m.transferNow()
		out = ConsolidationState{ID: NewID(), RoomID: target.RoomID, Target: target.ID, Sessions: ids, Profile: profileName, Model: chosen.Model, Phase: "waiting", Requested: now, Deadline: now.Add(30 * time.Second)}
		metas := []Meta{}
		for _, id := range ids {
			meta, e := m.store().ReadMeta(id)
			if e != nil {
				return e
			}
			if !meta.Closed.IsZero() || meta.ConsumedBy != "" {
				return ErrClosed
			}
			if meta.Role == RoleRoot {
				return errors.New("the internal root holds the room's root seat and cannot be consolidated; use Transfer to change its model")
			}
			if meta.RoomID != target.RoomID {
				return errors.New("sessions must belong to the same room")
			}
			if meta.Closing.Active() {
				return errors.New("session has a dormant close reservation")
			}
			if meta.Consolidating != "" || meta.Transfer.Active() || meta.Consolidation.Active() {
				return errors.New("a selected session is already transferring or consolidating")
			}
			metas = append(metas, meta)
			out.Sources = append(out.Sources, ConsolidationSource{Session: id, Address: meta.Address(), Phase: "waiting", Cursor: meta.RoomCursor})
		}
		if e := m.consolidationHierarchyPlan(out, false); e != nil {
			return e
		}
		for i := range out.Sources {
			meta := metas[i]
			previousModel := meta.Model
			if previousModel == "" {
				if prior, ok := ps.Get(meta.Profile); ok {
					previousModel = prior.Model
				}
			}
			token := consolidationToken(out.ID, meta.ID)
			out.Sources[i].Reservation = &TransferState{ID: token, Session: meta.ID, Phase: "waiting", Profile: profileName, Model: chosen.Model, PreviousProfile: meta.Profile, PreviousModel: previousModel, PreviousHarness: meta.HarnessSession, PreviousDir: meta.Dir, PreviousResumable: meta.Resumable, OldTurns: meta.Turns, Requested: now, Deadline: out.Deadline, PostID: transferPostID(token)}
		}
		if kept.Consolidation != nil {
			kept.ConsolidationHistory = append(kept.ConsolidationHistory, *kept.Consolidation)
		}
		// Write the recoverable plan before individual reservations. No runner can
		// acquire a selected metadata lock until the complete set is released.
		kept.Consolidation = &out
		if e := m.store().writeMeta(kept); e != nil {
			return e
		}
		for _, meta := range metas {
			if meta.ID == kept.ID {
				meta.Consolidation = &out
				meta.ConsolidationHistory = kept.ConsolidationHistory
			}
			meta.Consolidating = out.ID
			token := consolidationToken(out.ID, meta.ID)
			previousModel := meta.Model
			if previousModel == "" {
				if prior, ok := ps.Get(meta.Profile); ok {
					previousModel = prior.Model
				}
			}
			for n := 1; n <= meta.Turns; n++ {
				turn, e := m.store().readTurnForUpdate(meta.ID, n)
				if e != nil {
					return e
				}
				if turn.Profile == "" {
					turn.Profile, turn.Model = meta.Profile, previousModel
					if e = m.store().writeTurn(meta.ID, turn); e != nil {
						return e
					}
				}
			}
			meta.Transfer = &TransferState{ID: token, Session: meta.ID, Phase: "waiting", Profile: profileName, Model: chosen.Model, PreviousProfile: meta.Profile, PreviousModel: previousModel, PreviousHarness: meta.HarnessSession, PreviousDir: meta.Dir, PreviousResumable: meta.Resumable, OldTurns: meta.Turns, Requested: now, Deadline: out.Deadline, PostID: transferPostID(token)}
			if e := m.store().writeMeta(meta); e != nil {
				return e
			}
		}
		return nil
	})
	unlock()
	if err != nil {
		return out, err
	}
	if out.Active() {
		err = m.launchConsolidation(out)
	}
	return out, err
}
func (m *Manager) SessionConsolidations(roomID string) ([]ConsolidationState, error) {
	all, err := m.store().List(true)
	if err != nil {
		return nil, err
	}
	out := []ConsolidationState{}
	for _, info := range all {
		if info.RoomID == roomID && info.Consolidation != nil {
			out = append(out, info.ConsolidationHistory...)
			out = append(out, *info.Consolidation)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requested.After(out[j].Requested) })
	return out, nil
}
func (m *Manager) consolidation(roomID, id string) (ConsolidationState, error) {
	all, err := m.SessionConsolidations(roomID)
	if err != nil {
		return ConsolidationState{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return ConsolidationState{}, errors.New("no such consolidation in this room")
}
func (m *Manager) updateConsolidation(target, id string, fn func(*ConsolidationState) error) error {
	return m.store().withLock(target, func() error {
		meta, e := m.store().ReadMeta(target)
		if e != nil {
			return e
		}
		if meta.Consolidation == nil || meta.Consolidation.ID != id {
			return errors.New("stale consolidation")
		}
		if e = fn(meta.Consolidation); e != nil {
			return e
		}
		return m.store().writeMeta(meta)
	})
}
func (m *Manager) launchConsolidation(c ConsolidationState) error {
	launch := m.ConsolidationLaunch
	if launch == nil {
		launch = goConsolidation
	}
	if err := launch(m, c.Target, c.ID); err != nil {
		_ = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
			s.Error = "Worker paused: " + err.Error() + ". Resume this consolidation."
			return nil
		})
		return fmt.Errorf("consolidation saved; resume %s: %w", c.ID, err)
	}
	return nil
}

// goConsolidation uses the same engine-owned lifetime as goTransfer.
func goConsolidation(m *Manager, target, id string) error {
	m.workers().Go("consolidation "+target+" "+id, func(ctx context.Context) error {
		return m.runConsolidation(ctx, target, id)
	})
	return nil
}
func (m *Manager) ResumeConsolidation(ctx context.Context, roomID, id string) (ConsolidationState, error) {
	if err := authority.Check("subagent consolidate resume", authority.Of(ctx), nil); err != nil {
		return ConsolidationState{}, err
	}
	c, err := m.consolidation(roomID, id)
	if err != nil {
		return c, err
	}
	if c.Phase == "cancelled" && c.CancelPending {
		return m.CancelConsolidation(ctx, roomID, id)
	}
	if c.Phase == "failed" {
		release, e := m.consolidationWorkerLock(c.Target)
		if e != nil {
			if errors.Is(e, syscall.EWOULDBLOCK) {
				return c, nil
			}
			return c, e
		}
		err = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
			if s.Phase != "failed" {
				return nil
			}
			s.Phase = "stopping"
			s.Error = ""
			s.Ended = time.Time{}
			return nil
		})
		release()
		if err != nil {
			return c, err
		}
		c, _ = m.consolidation(roomID, id)
	}
	if c.Active() {
		err = m.launchConsolidation(c)
	}
	return c, err
}
func (m *Manager) CancelConsolidation(ctx context.Context, roomID, id string) (ConsolidationState, error) {
	if err := authority.Check("subagent consolidate cancel", authority.Of(ctx), nil); err != nil {
		return ConsolidationState{}, err
	}
	c, err := m.consolidation(roomID, id)
	if err != nil {
		return c, err
	}
	if c.Phase == "cancelled" && !c.CancelPending {
		return c, nil
	}
	unlock, err := m.consolidationWorkerLock(c.Target)
	if err != nil {
		return c, errors.New("consolidation is advancing; retry cancellation")
	}
	defer unlock()
	c, err = m.consolidation(roomID, id)
	if err != nil {
		return c, err
	}
	if c.Phase == "cancelled" && !c.CancelPending {
		return c, nil
	}

	if err = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
		if s.Phase == "cancelled" {
			return nil
		}
		// A paused consolidation (one with a saved error) can be cancelled
		// before the commit starts, while its sources still hold their
		// reservations. A committing one may have changed hierarchy and closed
		// sources, so it can only resume.
		if s.Phase != "waiting" && s.Phase != "failed" && (s.Error == "" || s.Phase == "committing") {
			return errors.New("startup has begun; resume consolidation to finish safely")
		}
		s.Phase = "cancelled"
		s.CancelPending = true
		return nil
	}); err != nil {
		return c, err
	}
	for _, sid := range c.Sessions {
		err = m.store().withLock(sid, func() error {
			meta, e := m.store().ReadMeta(sid)
			if e != nil {
				return e
			}
			// A completed cleanup can release this source before another cleanup
			// attempt resumes. Never inspect or delete its later transfer request.
			if meta.Consolidating != c.ID {
				return nil
			}
			if meta.Transfer != nil && meta.Transfer.ID == consolidationToken(c.ID, sid) {
				_ = m.store().deletePendingSteer(m.rooms(), meta, meta.Transfer.PostID)
				meta.Transfer.Phase = "cancelled"
			}
			meta.Consolidating = ""
			return m.store().writeMeta(meta)
		})
		if err != nil {
			return c, err
		}
	}
	err = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error { s.CancelPending = false; s.Ended = m.transferNow(); return nil })
	if err != nil {
		return c, err
	}
	return m.consolidation(roomID, id)
}
// postConsolidationPaused tells root that a consolidation stopped on an error,
// with the two commands that move it on.
func (m *Manager) postConsolidationPaused(c ConsolidationState, cause error) {
	meta, err := m.store().ReadMeta(c.Target)
	if err != nil {
		log.Printf("consolidation alert: %v", err)
		return
	}
	event := room.AlertEvent{
		Source:  room.AlertConsolidation,
		Summary: fmt.Sprintf("Consolidation %s paused in phase %s: %v", c.ID, c.Phase, cause),
		Remedy:  "dyna subagent consolidate resume " + c.ID,
		Detail:  fmt.Sprintf("Consolidation %s into %s paused in phase %s: %v. Resume it with `dyna subagent consolidate resume %s`, or stop it with `dyna subagent consolidate cancel %s`.", c.ID, c.Target, c.Phase, cause, c.ID, c.ID),
	}
	if _, err := m.rooms().PostEventInput(room.PostEventInput{RoomID: meta.RoomID, Event: event, Producer: room.ProducerEngine, NeedsRoot: true, To: []string{meta.Room + ":" + RoleRoot}}); err != nil {
		log.Printf("consolidation alert: %v", err)
	}
}

func (m *Manager) consolidationWorkerLock(target string) (func(), error) {
	dir, err := m.store().Dir(target)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "consolidation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Failed operations and cancelled cleanup keep their explicit resume paths.
var consolidationWorkerPhases = []string{"waiting", "stopping", "starting", "committing"}

// runConsolidation advances the engine's own durable work, without a caller.
func (m *Manager) runConsolidation(ctx context.Context, target, id string) error {
	// The worker lock is held for one bounded step, allowing cancellation while
	// waiting without racing a startup transition.
	delay := 200 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, e := m.store().ReadMeta(target)
		if e != nil {
			return e
		}
		if e = m.waitRoom(ctx, current.RoomID); e != nil {
			return e
		}
		unlock, err := m.consolidationWorkerLock(target)
		if err != nil {
			if errors.Is(err, syscall.EWOULDBLOCK) {
				if err := m.transferSleep(ctx, delay); err != nil {
					return err
				}
				delay = min(delay*2, time.Second)
				continue
			}
			return err
		}
		meta, err := m.store().ReadMeta(target)
		if err != nil {
			unlock()
			return err
		}
		before := meta.Consolidation
		beforeTransfer, beforeTurns := meta.Transfer, meta.Turns
		if before == nil || before.ID != id || !slices.Contains(consolidationWorkerPhases, before.Phase) {
			unlock()
			return nil
		}
		done, err := m.advanceConsolidation(ctx, *before)
		unlock()
		if errors.Is(err, errRoomPaused) {
			continue
		}
		if err != nil {
			_ = m.updateConsolidation(target, id, func(c *ConsolidationState) error {
				c.Error = "Paused: " + err.Error() + ". Resume to continue."
				return nil
			})
			m.postConsolidationPaused(*before, err)
			return err
		}
		if done {
			return nil
		}
		meta, err = m.store().ReadMeta(target)
		if err != nil {
			return err
		}
		after := meta.Consolidation
		if after == nil || after.ID != id || !slices.Contains(consolidationWorkerPhases, after.Phase) {
			return nil
		}
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeTransfer, meta.Transfer) || beforeTurns != meta.Turns {
			delay = 200 * time.Millisecond
			continue
		}
		pause := delay
		if after.Phase == "waiting" {
			remaining := after.Deadline.Sub(m.transferNow())
			if remaining <= 0 {
				continue
			}
			pause = min(pause, remaining)
		}
		if err = m.transferSleep(ctx, pause); err != nil {
			return err
		}
		delay = min(delay*2, time.Second)
	}
}
func (m *Manager) advanceConsolidation(ctx context.Context, c ConsolidationState) (bool, error) {
	budget, err := m.coordinatorPauseBudget(ctx, c.RoomID, c.Requested)
	if err != nil {
		return false, err
	}
	if budget > c.PausedFor {
		err = m.updateConsolidation(c.Target, c.ID, func(current *ConsolidationState) error {
			delta := budget - current.PausedFor
			if delta > 0 {
				current.Deadline = current.Deadline.Add(delta)
				current.PausedFor = budget
			}
			c = *current
			return nil
		})
		if err != nil {
			return false, err
		}
	}

	switch c.Phase {
	case "waiting":
		if err := m.reserveConsolidation(c); err != nil {
			return false, err
		}
		ready := true
		for _, sid := range c.Sessions {
			meta, e := m.store().ReadMeta(sid)
			if e != nil {
				return false, e
			}
			if meta.Consolidating != c.ID {
				return false, errors.New("incomplete reservation; resume requires intact selected sessions")
			}
			if meta.Transfer == nil || meta.Transfer.ID != consolidationToken(c.ID, sid) {
				return false, errors.New("source transfer reservation changed")
			}
			if meta.Transfer.Phase == "waiting" {
				if _, e = m.advanceTransfer(ctx, sid, meta.Transfer.ID); e != nil {
					return false, e
				}
				meta, e = m.store().ReadMeta(sid)
				if e != nil {
					return false, e
				}
			}
			if meta.Transfer.Phase == "waiting" {
				ready = false
			}
		}
		err := m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
			for i := range s.Sources {
				meta, e := m.store().ReadMeta(s.Sources[i].Session)
				if e != nil {
					return e
				}
				s.Sources[i].Phase = meta.Transfer.Phase
				s.Sources[i].Source = meta.Transfer.Source
			}
			if ready {
				s.Phase = "stopping"
			}
			return nil
		})
		return false, err
	case "stopping":
		// Freeze every source first. No new source work can race the stop sweep.
		for _, sid := range c.Sessions {
			meta, e := m.store().ReadMeta(sid)
			if e != nil {
				return false, e
			}
			if meta.Transfer == nil {
				return false, errors.New("missing source reservation")
			}
			if e = m.updateTransfer(sid, meta.Transfer.ID, func(_ *Meta, t *TransferState) error { t.Phase = "stopping"; return nil }); e != nil {
				return false, e
			}
		}
		for _, sid := range c.Sessions {
			meta, e := m.store().ReadMeta(sid)
			if e != nil {
				return false, e
			}
			_ = m.store().DeletePendingSteer(m.rooms(), sid, meta.Transfer.PostID)
			if m.store().lockHeld(sid) {
				stopCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				_, e := m.stopSession(stopCtx, sid, c.ID, "consolidation")
				cancel()
				if e != nil {
					return false, e
				}
			}
		}

		sources := append([]ConsolidationSource(nil), c.Sources...)
		targetDir := ""
		for i := range sources {
			meta, e := m.store().ReadMeta(sources[i].Session)
			if e != nil {
				return false, e
			}
			t := *meta.Transfer
			if latest, e := m.store().ReadTurn(meta.ID, meta.Turns); e == nil && t.WrittenHandoff == "" && latest.Ended.After(t.Requested) {
				if text, ok := latest.Result.(string); ok && strings.HasPrefix(strings.TrimSpace(text), "[DYNA HANDOFF]") {
					t.Handoff, t.WrittenHandoff, t.Source = text, text, "cooperative"
				}
			}
			if t.Source == "" {
				t.Source = "transcript"
			}
			packet, dir, e := m.handoffPacket(meta, t)
			if e != nil {
				return false, e
			}
			queue, e := m.store().SessionQueue(m.rooms(), meta.ID)
			if e != nil {
				return false, e
			}
			sources[i].PendingIDs = nil
			for _, item := range queue {
				if (item.State == "waiting" || item.State == "delivering") && item.ID != t.PostID {
					sources[i].PendingIDs = append(sources[i].PendingIDs, item.ID)
				}
			}
			sources[i].Context, sources[i].Dir, sources[i].Cursor, sources[i].Source, sources[i].Phase = packet, dir, meta.RoomCursor, t.Source, "ready"
			if meta.ID == c.Target {
				targetDir = dir
			}
		}
		if c.Charter != "" {
			sources = c.Sources
			for _, source := range sources {
				if source.Session == c.Target {
					targetDir = source.Dir
				}
			}
		}
		err := m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
			s.Sources = sources
			s.Charter = m.consolidationCharter(c)
			s.Phase = "starting"
			s.Error = ""
			return nil
		})
		if err != nil {
			return false, err
		}
		token := consolidationToken(c.ID, c.Target)
		err = m.updateTransfer(c.Target, token, func(meta *Meta, t *TransferState) error {
			t.Phase = "starting"
			t.Profile, t.Model = c.Profile, c.Model
			t.Turn = meta.Turns + 1
			t.OldTurns = meta.Turns
			t.StartupDeadline = m.transferNow().Add(startupLimit)
			// The charter itself refreshes into the instructions on this turn.
			t.Handoff = "[DYNA SESSION CONSOLIDATION] Your combined task charter is in your Dyna instructions. Continue it now."
			t.Dir = targetDir
			meta.Profile, meta.Model, meta.HarnessSession, meta.Dir = c.Profile, c.Model, "", targetDir
			ps, e := m.profiles()
			if e != nil {
				return e
			}
			p, ok := ps.Get(c.Profile)
			if !ok || p.Disabled {
				return errors.New("target profile unavailable")
			}
			chosen := *p
			chosen.Model = c.Model
			chosen.Models = nil
			meta.Resumable = harness.Resumable(chosen)
			return nil
		})
		return false, err
	case "starting":
		meta, e := m.store().ReadMeta(c.Target)
		if e != nil {
			return false, e
		}
		t := meta.Transfer
		// A crash between the state and transfer writes recovers the saved packet.
		if t == nil {
			return false, errors.New("missing target transfer")
		}
		if t.Phase == "stopping" {
			return false, m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error { s.Phase = "stopping"; return nil })
		}
		if _, e = m.advanceTransfer(ctx, c.Target, t.ID); e != nil {
			fresh, _ := m.store().ReadMeta(c.Target)
			if fresh.Transfer != nil && fresh.Transfer.Phase == "failed" {
				_ = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
					s.Phase = "failed"
					s.Error = e.Error()
					s.Ended = m.transferNow()
					return nil
				})
				m.postConsolidationPaused(c, e)
				return true, nil
			}
			return false, e
		}
		fresh, e := m.store().ReadMeta(c.Target)
		if e != nil {
			return false, e
		}
		if fresh.Transfer.Phase == "completed" {
			if e = m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error { s.Phase = "committing"; return nil }); e != nil {
				return false, e
			}
			// Accept startup and archive the sources in this same step.
			// Persist committing first so an interrupted commit can resume.
			if e = m.commitConsolidation(c); e != nil {
				return false, e
			}
			return true, nil
		}
		return false, nil
	case "committing":
		if err := m.commitConsolidation(c); err != nil {
			return false, err
		}
		return true, nil
	}
	return true, nil
}

// consolidationCharter is rebuilt from references at every lifecycle boundary,
// including startup retries. A saved charter is never replacement evidence.
func (m *Manager) consolidationCharter(c ConsolidationState) string {
	var b strings.Builder
	b.WriteString("[DYNA SESSION CONSOLIDATION]\nContinue the selected tasks as one agent in this stable session. Review every source before acting. Original histories and worktrees remain readable. Do not merge or delete source worktrees. Absorbed agents no longer work in parallel. Inherited reviewer tasks do not authorize self-review; use an independent reviewer when roles collapse. Your kept session owns these tasks; source identities describe their old roles.\n")
	fmt.Fprintf(&b, "Saved source context and worktree paths: `dyna subagent consolidate status %s --json`.\n", c.ID)
	for _, source := range c.Sources {
		meta, err := m.store().ReadMeta(source.Session)
		if err != nil || source.Reservation == nil {
			fmt.Fprintf(&b, "Review source session %s with `dyna subagent show %s --json` before acting.\n", source.Session, source.Session)
			continue
		}
		// The kept session advances too. Its output reference must use the
		// saved source turn, rather than changing its instructions every turn.
		b.WriteString(m.sourceReview(meta, source.Reservation.OldTurns))
	}
	return b.String()
}

// consolidationPromptMeta previews durable references before startup succeeds.
// The commit installs the same plan only after the replacement has started.
func (m *Manager) consolidationPromptMeta(meta Meta, c ConsolidationState) (Meta, error) {
	meta.ConsolidationCharter = m.consolidationCharter(c)
	meta.InheritedInboxes = append([]InheritedInbox(nil), meta.InheritedInboxes...)
	add := func(in InheritedInbox) {
		for _, old := range meta.InheritedInboxes {
			if old.Session == in.Session {
				return
			}
		}
		meta.InheritedInboxes = append(meta.InheritedInboxes, in)
	}
	for _, source := range c.Sources {
		old, err := m.store().ReadMeta(source.Session)
		if err != nil {
			return meta, err
		}
		if source.Session != c.Target {
			add(InheritedInbox{Dir: source.Dir, Session: source.Session, Address: source.Address, Cursor: source.Cursor, PendingIDs: source.PendingIDs})
		}
		for _, in := range old.InheritedInboxes {
			add(in)
		}
	}
	return meta, nil
}

func (m *Manager) commitConsolidation(c ConsolidationState) error {
	unlock, err := lockNames(m.store())
	if err != nil {
		return err
	}
	defer unlock()
	// Resolve the destination once, before changing hierarchy or closing any
	// source. Other assignments must not override the survivor's spawn goal.
	targetMeta, err := m.store().ReadMeta(c.Target)
	if err != nil {
		return err
	}
	toGoal, err := m.goalStore().EffectiveGoal(c.RoomID, c.Target, targetMeta.Goal)
	if err != nil {
		return fmt.Errorf("cannot fold goals into %s: %w", c.Target, err)
	}
	if toGoal != "" {
		g, err := m.goalStore().Get(c.RoomID, toGoal)
		if err != nil {
			return err
		}
		if !g.Open() {
			// A finished or dropped goal takes no folded items. The absorbed
			// goals stay as they are; the rest of the commit continues.
			toGoal = ""
		}
	}
	if err = m.consolidationHierarchy(c); err != nil {
		return err
	}
	absorbed := map[string]bool{}
	for _, sid := range c.Sessions {
		if sid != c.Target {
			absorbed[sid] = true
		}
	}
	if err = links.Consolidate(state.Root(filepath.Dir(m.store().root())), c.RoomID, c.Target, absorbed); err != nil {
		return err
	}

	// Build a complete inbox and receipt plan before writing any closure.
	planned, err := m.consolidationPromptMeta(targetMeta, c)
	if err != nil {
		return err
	}
	inboxes := planned.InheritedInboxes
	receipts := map[string]bool{}
	for id, received := range targetMeta.InheritedReceipts {
		receipts[id] = received
	}
	for _, source := range c.Sources {
		old, e := m.store().ReadMeta(source.Session)
		if e != nil {
			return e
		}
		for id, received := range old.InheritedReceipts {
			if received {
				receipts[id] = true
			}
		}
		if len(old.InheritedInboxes) > 0 {
			delivered, e := m.store().deliveredIDs(old.ID, old.Turns)
			if e != nil {
				return e
			}
			for id := range delivered {
				receipts[id] = true
			}
		}
	}
	// Install references before closing sources. Replaying this step is safe.
	_, err = m.store().UpdateMeta(c.Target, func(meta *Meta) {
		meta.InheritedInboxes = inboxes
		meta.InheritedReceipts = receipts
		meta.ConsolidationCharter = m.consolidationCharter(c)
	})
	if err != nil {
		return err
	}
	for _, sid := range c.Sessions {
		if sid == c.Target {
			continue
		}
		_, err = m.store().UpdateMeta(sid, func(meta *Meta) {
			meta.ConsumedBy = c.Target
			meta.Closed = m.transferNow()
			meta.Consolidating = ""
			if meta.Transfer != nil {
				meta.Transfer.Phase = "completed"
			}
		})
		if err != nil {
			return err
		}
		destroyComputer(sid)
		// The kept session continues the absorbed work: its checklist folds
		// into the kept session's effective goal, item assignees follow, and
		// open questions move with it.
		if err = m.foldGoalItems(c.RoomID, sid, c.Target, toGoal); err != nil {
			return err
		}
		if _, err = m.questionStore().Reassign(c.RoomID, sid, c.Target); err != nil {
			return err
		}
	}
	_, err = m.store().UpdateMeta(c.Target, func(meta *Meta) {
		meta.Consolidating = ""
		meta.Consolidation.Phase = "committing"
	})
	if err != nil {
		return err
	}
	return m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error { s.Phase, s.Error = "completed", ""; s.Ended = m.transferNow(); return nil })
}

// reserveConsolidation repairs a crash between the durable plan write and
// any individual reservation write. Never replace a later, unrelated owner.
func (m *Manager) reserveConsolidation(c ConsolidationState) error {
	unlock, err := lockNames(m.store())
	if err != nil {
		return err
	}
	defer unlock()
	return m.store().lockSessions(c.Sessions, func() error {
		for _, source := range c.Sources {
			meta, e := m.store().ReadMeta(source.Session)
			if e != nil {
				return e
			}
			if !meta.Closed.IsZero() || meta.ConsumedBy != "" {
				return ErrClosed
			}
			if meta.Consolidating != "" && meta.Consolidating != c.ID {
				return errors.New("selected session has another reservation")
			}
			if meta.Transfer.Active() && meta.Transfer.ID != consolidationToken(c.ID, meta.ID) {
				return errors.New("selected session has another transfer")
			}
		}
		for _, source := range c.Sources {
			meta, e := m.store().ReadMeta(source.Session)
			if e != nil {
				return e
			}
			if source.Reservation == nil {
				return errors.New("missing saved source profile")
			}
			for n := 1; n <= meta.Turns; n++ {
				turn, e := m.store().readTurnForUpdate(meta.ID, n)
				if e != nil {
					return e
				}
				if turn.Profile == "" {
					turn.Profile, turn.Model = source.Reservation.PreviousProfile, source.Reservation.PreviousModel
					if e = m.store().writeTurn(meta.ID, turn); e != nil {
						return e
					}
				}
			}
			meta.Consolidating = c.ID
			if meta.Transfer == nil || meta.Transfer.ID != consolidationToken(c.ID, meta.ID) {
				if source.Reservation == nil {
					return errors.New("missing saved reservation")
				}
				snapshot := *source.Reservation
				meta.Transfer = &snapshot
			}
			if e = m.store().writeMeta(meta); e != nil {
				return e
			}
		}
		return nil
	})
}

// Summary is the small polling/API view. Full packets remain available through
// the CLI status command and durable source histories rather than each poll.
func (c ConsolidationState) Summary() ConsolidationState {
	c.Charter = ""
	c.Sources = append([]ConsolidationSource(nil), c.Sources...)
	for i := range c.Sources {
		c.Sources[i].Context = ""
		c.Sources[i].Reservation = nil
		c.Sources[i].PendingIDs = nil
	}
	return c
}

// foldGoalItems merges one absorbed session's checklist into the kept
// session's validated goal: the absorbed goal closes as folded, its items
// move with fresh IDs, item assignees follow to the kept session, and
// session records that name a moved item remap. Goals the absorbed session
// held beyond its own return to their setter, as any close does.
func (m *Manager) foldGoalItems(room, from, to, toGoal string) error {
	store := m.goalStore()
	fromMeta, err := m.store().ReadMeta(from)
	if err != nil {
		return err
	}
	fromGoal := fromMeta.Goal
	if fromGoal != "" {
		if g, err := store.Get(room, fromGoal); err != nil || !g.Open() || !g.HeldBy(from) {
			fromGoal = ""
		}
	}
	if fromGoal != "" {
		if toGoal == "" || toGoal == fromGoal {
			toGoal = ""
		}
		if toGoal == "" {
			// No goal can take the checklist, so the kept session takes the
			// unfinished goal itself. Closing the source must not drop it.
			if _, err := store.HandoffHolder(room, fromGoal, from, to); err != nil {
				return err
			}
		}
		if toGoal != "" {
			moved, err := store.MoveItems(room, fromGoal, toGoal)
			if err != nil {
				return err
			}
			// Session records naming a moved item remap to the new IDs.
			sessions, err := m.store().List(false)
			if err != nil {
				return err
			}
			for _, s := range sessions {
				if s.RoomID != room || s.Goal != fromGoal || s.Item == "" {
					continue
				}
				if next, ok := moved[s.Item]; ok {
					_, err := m.store().UpdateMeta(s.ID, func(meta *Meta) {
						meta.Goal, meta.Item = toGoal, next
					})
					if err != nil {
						return err
					}
				}
			}
			target, _ := m.store().Info(to)
			who := to
			if target.ID != "" {
				who = target.Address()
			}
			if _, err := store.Finish(room, fromGoal, from, "folded into "+who, false); err != nil {
				return err
			}
		}
	}
	// Items the absorbed session was assigned on other goals follow to the
	// kept session, and any goal it held beyond its own returns to its
	// setter, exactly as closing it would do.
	if err := store.ReassignItems(room, from, to); err != nil {
		return err
	}
	m.closeGoals(Info{Meta: Meta{ID: from, RoomID: room}}, to)
	return nil
}
