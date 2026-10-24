// Package goals stores long-lasting room tasks and their journals.
package goals

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
)

type Edit struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	OldText string    `json:"oldText"`
}
type Outcome struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Summary string    `json:"summary"`
}
type Goal struct {
	ID   string `json:"id"`
	Room string `json:"room"`
	Text string `json:"text"`
	// Detail is the brief stored with a spawn goal: context, owned files,
	// checks and limits. Dyna resends it with Text after compaction.
	Detail  string   `json:"detail,omitempty"`
	Setter  string   `json:"setter"`
	Holders []string `json:"holders"`
	Shared  bool     `json:"shared"`
	// NextItem allocates the goal's item IDs: i1, i2, ...
	NextItem  int       `json:"nextItem,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Edits     []Edit    `json:"edits"`
	Done      *Outcome  `json:"done,omitempty"`
	// ExpediteUntil marks the room's one expedited goal until that time.
	// Readers drop an expired value; see Store.Expedite.
	ExpediteUntil *time.Time `json:"expediteUntil,omitempty"`
	// LegacyOwnerHeld marks a goal that an old build left with the owner. Root
	// must ask the owner about it, then drop or finish the goal, or hand it to
	// an open session. Nothing writes it now.
	// TODO: legacy/shim remove later — only goals saved by an old build carry it; remove once every room's owner-held goals are resolved.
	LegacyOwnerHeld bool `json:"legacyOwnerHeld,omitempty"`
}

func (g Goal) Open() bool { return g.State == "active" }

// NeedsRoot reports a goal that only root can move: root asks the owner about
// it, then drops or finishes it, or hands it to an open session.
func (g Goal) NeedsRoot() bool { return g.LegacyOwnerHeld && g.Open() }

// RootHoldsNothing is the refusal for any goal holder that is root. Root leads
// the room, so the agents that do the work hold the goals.
const RootHoldsNothing = "root cannot hold goals; pass --for @agent when creating with dyna goal"

// Concerns reports whether session id sees the goal: shared, held or set by it.
func (g Goal) Concerns(id string) bool { return g.Shared || g.HeldBy(id) || g.Setter == id }
func (g Goal) HeldBy(id string) bool {
	for _, h := range g.Holders {
		if h == id {
			return true
		}
	}
	return false
}

// RuleSet holds the standing rules of one scope: a room, or Global.
type RuleSet struct {
	Rules      map[string]*Rule `json:"rules,omitempty"`
	NextRuleID int              `json:"nextRuleId,omitempty"`
}
type RoomState struct {
	RuleSet
	NextID int              `json:"nextId"`
	Goals  map[string]*Goal `json:"goals"`
}
type State struct {
	Version int                   `json:"version"`
	Rooms   map[string]*RoomState `json:"rooms"`
	Global  *RuleSet              `json:"global,omitempty"`
}
type Store struct {
	Root   string
	Now    func() time.Time
	Issues *room.Store
}

func DefaultStore() *Store { return &Store{Root: filepath.Join(store.DataDir(), "goals")} }
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (st *State) Room(id string) *RoomState {
	if st.Rooms[id] == nil {
		st.Rooms[id] = &RoomState{Goals: map[string]*Goal{}}
	}
	r := st.Rooms[id]
	if r.Goals == nil {
		r.Goals = map[string]*Goal{}
	}
	return r
}

// List returns every goal of one room, oldest first.
func (s *Store) List(room string) ([]Goal, error) { return s.list(false, "room=?", room) }

// ListOpen returns the open goals of one room, oldest first.
func (s *Store) ListOpen(room string) ([]Goal, error) {
	return s.list(false, "room=? AND "+openGoals, room)
}
func (s *Store) Get(room, id string) (Goal, error) {
	found, err := s.list(false, "room=? AND id=?", room, id)
	if err != nil {
		return Goal{}, err
	}
	if len(found) == 0 {
		return Goal{}, fmt.Errorf("goal %s does not exist in this room", id)
	}
	return found[0], nil
}

// named selects open goals plus one named goal, for writers that read a
// closed goal.
func named(room, id string) (string, []any) {
	return openGoals + " OR room=? AND id=?", []any{room, id}
}

// CheckText reports why text cannot be a goal.
func CheckText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("goal text is required")
	}
	if utf8.RuneCountInString(text) > 2000 {
		return fmt.Errorf("goal text exceeds 2000 characters")
	}
	return nil
}

type Input struct {
	Text    string   `json:"text"`
	Detail  string   `json:"detail,omitempty"`
	Holders []string `json:"holders"`
	Shared  bool     `json:"shared"`
}

func (s *Store) Add(room, by string, in Input) (Goal, error) {
	var out Goal
	if room == "" {
		return out, fmt.Errorf("a room is required")
	}
	if err := CheckText(in.Text); err != nil {
		return out, err
	}
	err := s.updateTx(func(w *wake, st *State) error {
		r := st.Room(room)
		for _, id := range unique(append(append([]string{}, in.Holders...), by)) {
			if err := w.activate(id); err != nil {
				return fmt.Errorf("cannot add goal holder %s: %w", id, err)
			}
		}
		r.NextID++
		now := s.now()
		out = Goal{ID: fmt.Sprintf("g%d", r.NextID), Room: room, Text: strings.TrimSpace(in.Text), Detail: strings.TrimSpace(in.Detail), Setter: by, Holders: unique(in.Holders), Shared: in.Shared, State: "active", CreatedAt: now, UpdatedAt: now, Edits: []Edit{}}
		r.Goals[out.ID] = &out
		return nil
	}, openGoals)
	return out, err
}

// Edit changes text and, when holders is non-nil, replaces the holders.
func (s *Store) Edit(room, id, by, text string, holders *[]string, shared bool) (Goal, error) {
	return s.change(room, id, func(w *wake, _ *RoomState, g *Goal) error {
		if text == "" && holders == nil {
			return fmt.Errorf("supply new text or --for")
		}
		if text != "" {
			if err := CheckText(text); err != nil {
				return err
			}
		}
		nextHolders := g.Holders
		if holders != nil {
			nextHolders = *holders
		}
		for _, holder := range unique(append(append([]string{}, nextHolders...), by)) {
			if err := w.activate(holder); err != nil {
				// Editing text does not assign new work to an existing closed holder.
				if g.HeldBy(holder) && errors.Is(err, sessionlife.ErrClosed) {
					continue
				}
				return fmt.Errorf("cannot add goal holder %s: %w", holder, err)
			}
		}
		g.Edits = append(g.Edits, Edit{At: s.now(), By: by, OldText: g.Text})
		if text != "" {
			g.Text = strings.TrimSpace(text)
		}
		if holders != nil {
			g.Holders, g.Shared = unique(*holders), shared
			// New holders or the whole room settle an old owner-held mark.
			g.LegacyOwnerHeld = false
		}
		g.State, g.Done = "active", nil
		return nil
	})
}
func (s *Store) Finish(room, id, by, summary string, drop bool) (Goal, error) {
	return s.change(room, id, func(_ *wake, _ *RoomState, g *Goal) error {
		g.State, g.ExpediteUntil = "done", nil
		if drop {
			g.State = "dropped"
		}
		g.Done = &Outcome{At: s.now(), By: by, Summary: summary}
		return nil
	})
}

func (s *Store) change(room, id string, fn func(*wake, *RoomState, *Goal) error) (Goal, error) {
	var out Goal
	where, args := named(room, id)
	err := s.updateTx(func(w *wake, st *State) error {
		r := st.Room(room)
		g := r.Goals[id]
		if g == nil {
			return fmt.Errorf("goal %s does not exist in this room", id)
		}
		if err := fn(w, r, g); err != nil {
			return err
		}
		g.UpdatedAt = s.now()
		out = *g
		return nil
	}, where, args...)
	return out, err
}

// sessions is the sessions folder beside the goal store.
func (s *Store) sessions() string { return filepath.Join(filepath.Dir(s.Root), "sessions") }

func unique(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range in {
		if id != "" && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

// CloseSession drops the closing session's own work. Open work moves to a
// live session: its setter first, then the heir. A goal with no live session
// left to hold it is dropped with a note, because root and the owner hold no
// goals. Historical goals keep the heir mapping without waking that heir.
func (s *Store) CloseSession(room, id, heir string, open map[string]bool) error {
	return s.updateTx(func(w *wake, st *State) error {
		r := st.Rooms[room]
		if r == nil {
			return nil
		}
		drop := func(g *Goal, now time.Time, note string) {
			g.Holders, g.Shared, g.State, g.ExpediteUntil, g.UpdatedAt = []string{}, false, "dropped", nil, now
			g.Done = &Outcome{At: now, By: id, Summary: note}
		}
		for _, g := range r.Goals {
			if g.Open() && g.HeldBy(id) && len(g.Holders) == 1 {
				now := s.now()
				g.Holders, g.Shared, g.UpdatedAt = []string{}, false, now
				if g.Setter == id {
					drop(g, now, "The sole holder "+id+" closed its own goal.")
					continue
				}
				// The setter keeps the work it delegated, and the heir takes work
				// no live setter is left to own.
				target := ""
				for _, candidate := range []string{g.Setter, heir} {
					if candidate != "" && candidate != id && open[candidate] && w.activate(candidate) == nil {
						target = candidate
						break
					}
				}
				if target == "" {
					drop(g, now, "No open session was left to hold this goal after "+id+" closed.")
					continue
				}
				g.Holders = []string{target}
				continue
			}
			// Historical handovers validate recipients without assigning live work.
			check := w.check
			if g.Open() {
				check = w.activate
			}
			live := func(sid string) bool {
				return sid != "" && open[sid] && check(sid) == nil
			}
			changed := false
			if g.Setter == id && live(heir) {
				g.Setter = heir
				changed = true
			}
			if g.HeldBy(id) {
				h := []string{}
				for _, holder := range g.Holders {
					if holder != id {
						h = append(h, holder)
					}
				}
				g.Holders = h
				if len(h) == 0 {
					switch {
					case live(heir):
						g.Holders = []string{heir}
					case !g.Open():
						// A finished goal keeps its record; no session works it.
					case live(g.Setter):
						g.Holders, g.Shared = []string{g.Setter}, false
					default:
						drop(g, s.now(), "No open session was left to hold this goal after "+id+" closed.")
					}
				}
				changed = true
			}
			if changed {
				g.UpdatedAt = s.now()
			}
		}
		return nil
	}, openGoals+` OR room=?1 AND (json_extract(body,'$.setter')=?2 OR EXISTS (SELECT 1 FROM json_each(body,'$.holders') WHERE value=?2))`, room, id)
}

// OpenHeldBy lists open goals the session holds. Shared goals have no holder.
// It waits for any writer, so a lifecycle check sees committed handoffs.
func (s *Store) OpenHeldBy(room, sessionID string) ([]Goal, error) {
	open, err := s.list(true, "room=? AND "+openGoals, room)
	if err != nil {
		return nil, err
	}
	out := []Goal{}
	for _, g := range open {
		if g.HeldBy(sessionID) {
			out = append(out, g)
		}
	}
	return out, nil
}
