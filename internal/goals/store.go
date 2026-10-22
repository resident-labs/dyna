// Package goals stores long-lasting room tasks and the keeper's journal.
package goals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
)

type Edit struct {
	WaitUntil time.Time `json:"waitUntil,omitempty"`
	At        time.Time `json:"at"`
	By        string    `json:"by"`
	OldText   string    `json:"oldText"`
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
	Detail string `json:"detail,omitempty"`
	// TODO: legacy/shim remove later — marks goals that root and service sessions held before they stopped getting goals; remove with goalkeeper.dropServiceGoals.
	Service bool     `json:"service,omitempty"`
	Setter  string   `json:"setter"`
	Holders []string `json:"holders"`
	Shared  bool     `json:"shared"`
	// NextItem allocates the goal's item IDs: i1, i2, ...
	NextItem int `json:"nextItem,omitempty"`
	// NudgedAt records the last stale nudge, so a holder hears at most one
	// per stale window.
	NudgedAt  time.Time `json:"nudgedAt,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	WaitAt    time.Time `json:"waitAt,omitempty"`
	WaitUntil time.Time `json:"waitUntil,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Edits     []Edit    `json:"edits"`
	Done      *Outcome  `json:"done,omitempty"`
}

func (g Goal) Open() bool { return g.State == "active" }

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

// Notice is a queued keeper post. Kind is holder_closed, journal or alert.
// A notice stays queued until its post is stored.
type Notice struct {
	Kind string `json:"kind,omitempty"`
	// Entry is the journal entry a journal notice appends and posts.
	Entry *JournalEntry `json:"entry,omitempty"`
	// TODO: legacy/shim remove later — Intent tells holder-closed notices queued before notice kinds existed; remove when stored states hold none.
	Intent string `json:"intent,omitempty"`
	ID     string `json:"id"`
	Room   string `json:"room"`
	Goal   string `json:"goal"`
	To     string `json:"to,omitempty"`
	Text   string `json:"text"`
}

// RuleSet holds the standing rules of one scope: a room, or Global.
type RuleSet struct {
	Rules      map[string]*Rule `json:"rules,omitempty"`
	NextRuleID int              `json:"nextRuleId,omitempty"`
}
type RoomState struct {
	RuleSet
	// ReleasedAt records when each session last stopped holding an open
	// goal, whether it closed or moved to another holder.
	ReleasedAt map[string]time.Time `json:"releasedAt,omitempty"`
	Keeper     KeeperRoomState      `json:"keeper,omitempty"`
	NextID     int                  `json:"nextId"`
	Goals      map[string]*Goal     `json:"goals"`
	Notices    []Notice             `json:"notices"`
}
type State struct {
	Version int                   `json:"version"`
	Rooms   map[string]*RoomState `json:"rooms"`
	Global  *RuleSet              `json:"global,omitempty"`
}
type Store struct {
	Root string
	Now  func() time.Time
}

func DefaultStore() *Store { return &Store{Root: filepath.Join(store.DataDir(), "goals")} }
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// TODO: legacy/shim remove later — reads goals/state.json only for its one-time import into dyna.db; remove with importLegacy.
func readLegacy(path string) (State, error) {
	st := State{Version: 1, Rooms: map[string]*RoomState{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err = json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("invalid goals state: %w", err)
	}
	if st.Version != 1 {
		return st, fmt.Errorf("unsupported goals state version %d", st.Version)
	}
	if st.Rooms == nil {
		st.Rooms = map[string]*RoomState{}
	}
	for _, r := range st.Rooms {
		for _, g := range r.Goals {
			// TODO: legacy/shim remove later — goals stored as "resting" read as active; remove once no stored state has it
			if g.State == "resting" {
				g.State = "active"
			}
		}
	}
	return st, nil
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
		}
		g.State, g.Done = "active", nil
		return nil
	})
}
func (s *Store) Finish(room, id, by, summary string, drop bool) (Goal, error) {
	return s.change(room, id, func(_ *wake, _ *RoomState, g *Goal) error {
		g.State = "done"
		if drop {
			g.State = "dropped"
		}
		g.Done = &Outcome{At: s.now(), By: by, Summary: summary}
		return nil
	})
}

// MarkNudged records a stale nudge time on an open goal.
func (s *Store) MarkNudged(room, id string, at time.Time) error {
	return s.update(func(st *State) error {
		g := st.Room(room).Goals[id]
		if g == nil || !g.Open() {
			return fmt.Errorf("goal %s is not open in this room", id)
		}
		g.NudgedAt, g.UpdatedAt = at, s.now()
		return nil
	}, openGoals)
}

// Check reads a goal for a keeper sweep. Checking is not progress and does
// not move a strike deadline.
func (s *Store) Check(room, id string) (Goal, error) {
	g, err := s.Get(room, id)
	if err != nil {
		return Goal{}, err
	}
	if !g.Open() {
		return Goal{}, fmt.Errorf("edit this goal to reopen it before checking")
	}
	return g, nil
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

// CloseSession drops the closing session's own work. Delegated work returns
// to its live setter, or gets one owner escalation. Historical goals retain
// the existing heir mapping without waking that heir.
func (s *Store) CloseSession(room, id, heir string, open map[string]bool, completedResult string) error {
	return s.updateTx(func(w *wake, st *State) error {
		r := st.Rooms[room]
		if r == nil {
			return nil
		}
		for _, g := range r.Goals {
			if g.Open() && g.HeldBy(id) && len(g.Holders) == 1 {
				now := s.now()
				g.Holders, g.Shared, g.UpdatedAt = []string{}, false, now
				if g.Setter == id {
					g.State = "dropped"
					g.Done = &Outcome{At: now, By: id, Summary: "The sole holder " + id + " closed its own goal."}
					continue
				}
				target := ""
				if g.Setter != "" && g.Setter != "owner" && open[g.Setter] && w.activate(g.Setter) == nil {
					target = g.Setter
					g.Holders = []string{target}
				}
				text := "Goal " + g.ID + " lost its holder " + id + ". Take it, reassign it, or close it."
				if completedResult != "" {
					text = "Goal " + g.ID + " holder " + id + " closed after a successful turn.\n" + completedResult + "\nReview the result before reassigning or closing the goal."
				}
				r.Notices = append(r.Notices, Notice{Kind: "holder_closed", ID: fmt.Sprintf("goal_%s_holder_closed_%d", g.ID, now.UnixNano()), Room: room, Goal: g.ID, To: target, Text: text})
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
			if g.Setter == id {
				g.Setter = "owner"
				if live(heir) {
					g.Setter = heir
				}
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
						g.Holders, g.Shared = []string{heir}, false
					case live(g.Setter):
						g.Holders, g.Shared = []string{g.Setter}, false
					default:
						g.Shared = true
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
