package goals

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/sessionlife"
)

// WorkRef names a goal, or an item on that goal.
type WorkRef struct {
	Goal string `json:"goal"`
	Item string `json:"item,omitempty"`
}

func (r WorkRef) String() string {
	if r.Item != "" {
		return r.Goal + "/" + r.Item
	}
	return r.Goal
}

type ItemMove struct {
	From WorkRef `json:"from"`
	To   WorkRef `json:"to"`
}

// MoveItem moves one open item, or promotes it to a standalone goal when to
// is "new". Item rows, spawn metadata and the delivery notice commit together.
func (s *Store) MoveItem(room, goal, item, by, to string) (ItemMove, error) {
	var out ItemMove
	if to == "" {
		return out, fmt.Errorf("a destination goal or new is required")
	}
	if to == goal {
		return out, fmt.Errorf("item %s is already on goal %s", item, goal)
	}
	var events []Event
	err := s.updateTx(func(w *wake, st *State) error {
		r := st.Room(room)
		source := r.Goals[goal]
		if source == nil || !source.Open() {
			return fmt.Errorf("source goal %s is not open in this room", goal)
		}
		it, found, err := readItem(w.conn, room, goal, item)
		if err != nil {
			return err
		}
		if !found || it.State != ItemOpen {
			return fmt.Errorf("item %s is not open on goal %s", item, goal)
		}
		out = ItemMove{From: WorkRef{Goal: goal, Item: item}}
		now := s.now()
		if to == "new" {
			holder := it.Assignee
			if holder == "" {
				holder = by
			}
			for _, id := range unique([]string{holder, by}) {
				if err := w.activate(id); err != nil {
					return fmt.Errorf("cannot promote item for %s: %w", id, err)
				}
			}
			r.NextID++
			text, detail := promotedText(it.Text)
			g := &Goal{ID: fmt.Sprintf("g%d", r.NextID), Room: room, Text: text, Detail: detail, Setter: by, Holders: []string{holder}, State: "active", CreatedAt: now, UpdatedAt: now, Edits: []Edit{}}
			r.Goals[g.ID] = g
			out.To = WorkRef{Goal: g.ID}
			if _, err := w.conn.ExecContext(context.Background(), "DELETE FROM goal_items WHERE room=? AND goal=? AND id=?", room, goal, item); err != nil {
				return err
			}
		} else {
			destination := r.Goals[to]
			if destination == nil || !destination.Open() {
				return fmt.Errorf("destination goal %s is not open in this room", to)
			}
			if it.Assignee != "" {
				if err := w.activate(it.Assignee); err != nil {
					return fmt.Errorf("cannot move item for %s: %w", it.Assignee, err)
				}
			}
			destination.NextItem++
			next := fmt.Sprintf("i%d", destination.NextItem)
			if next == item {
				destination.NextItem++
				next = fmt.Sprintf("i%d", destination.NextItem)
			}
			out.To = WorkRef{Goal: to, Item: next}
			if err := moveItemRow(w.conn, room, goal, item, to, next, now); err != nil {
				return err
			}
			destination.UpdatedAt = now
		}
		if err := sessionlife.MoveSpawnItemTx(w.conn, room, goal, item, out.To.Goal, out.To.Item, w.lock); err != nil {
			return err
		}
		source.UpdatedAt = now
		text := fmt.Sprintf("Moved %s to %s.", out.From, out.To)
		events = []Event{
			{Goal: goal, Item: item, By: by, Event: "moved-out", Text: text, At: now},
			{Goal: out.To.Goal, Item: out.To.Item, By: by, Event: "moved-in", Text: text, At: now},
		}
		if it.Assignee != "" {
			command := fmt.Sprintf("dyna goal item done %s %s", out.To.Goal, out.To.Item)
			if out.To.Item == "" {
				command = fmt.Sprintf("dyna goal done %s \"<summary>\"", out.To.Goal)
			}
			r.Notices = append(r.Notices, Notice{Kind: "item_moved", ID: fmt.Sprintf("goal_move_%s_%s_%s_%s", goal, item, out.To.Goal, out.To.Item), Room: room, Goal: out.To.Goal, To: it.Assignee, Text: text + " When the work is done, run `" + command + "`."})
		}
		return nil
	}, "room=?", room)
	if err != nil {
		return ItemMove{}, err
	}
	// Keep prior events at the source and append a link at both locations.
	for _, ev := range events {
		if _, err := s.AddEvent(room, ev); err != nil {
			return out, err
		}
	}
	return out, nil
}

// promotedText keeps an oversized outcome intact in Detail. The headline
// stays within the goal limit; the complete item remains in the spawn prompt.
func promotedText(text string) (string, string) {
	head, brief, _ := strings.Cut(text, "\n\nBrief:\n")
	if strings.TrimSpace(head) == "" || utf8.RuneCountInString(head) > 2000 {
		return Clip(text, 2000), text
	}
	return head, brief
}
