package goals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// An item is one line of delegated work on a goal. State is open, done or
// dropped, and list order is the numeric item suffix (i2 before i10).
type Item struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	State     string    `json:"state"`
	Assignee  string    `json:"assignee,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const (
	ItemOpen    = "open"
	ItemDone    = "done"
	ItemDropped = "dropped"
)

// checkItemText caps an item's full brief, which folds the goal text and
// the brief into one field.
func checkItemText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("item text is required")
	}
	if utf8.RuneCountInString(text) > 16000 {
		return fmt.Errorf("item text exceeds 16000 characters")
	}
	return nil
}

// itemOrder lists items in suffix order, not text order, so i2 sorts before
// i10.
const itemOrder = " ORDER BY CAST(SUBSTR(id,2) AS INTEGER)"

// AddItem appends an item to an open goal, waking its assignee. The added
// event is appended after the item commits, so a lost event costs a nudge,
// not the item.
func (s *Store) AddItem(room, goal, by, text, assignee string) (Item, error) {
	var out Item
	if err := checkItemText(text); err != nil {
		return out, err
	}
	var ev Event
	where, args := named(room, goal)
	err := s.updateTx(func(w *wake, st *State) error {
		g := st.Room(room).Goals[goal]
		if g == nil {
			return fmt.Errorf("goal %s does not exist in this room", goal)
		}
		if !g.Open() {
			return fmt.Errorf("goal %s is not open", goal)
		}
		if assignee != "" {
			if err := w.activate(assignee); err != nil {
				return fmt.Errorf("cannot assign item %s to %s: %w", goal, assignee, err)
			}
		}
		now := s.now()
		g.NextItem++
		g.UpdatedAt = now
		out = Item{ID: fmt.Sprintf("i%d", g.NextItem), Text: strings.TrimSpace(text), State: ItemOpen, Assignee: assignee, UpdatedAt: now}
		if err := writeItem(w.conn, room, goal, out); err != nil {
			return err
		}
		ev = Event{Goal: goal, Item: out.ID, By: by, Kind: "event", Event: "added", At: now}
		return nil
	}, where, args...)
	if err != nil {
		return Item{}, err
	}
	if ev.Goal == "" {
		return out, nil
	}
	_, err = s.AddEvent(room, ev)
	return out, err
}

// SetItemState marks an item done, dropped or open again. Setting the state
// an item already has changes nothing.
func (s *Store) SetItemState(room, goal, item, by, state string) (Item, error) {
	var out Item
	if state != ItemOpen && state != ItemDone && state != ItemDropped {
		return out, fmt.Errorf("item state must be open, done or dropped")
	}
	var ev Event
	where, args := named(room, goal)
	err := s.updateTx(func(w *wake, st *State) error {
		if st.Room(room).Goals[goal] == nil {
			return fmt.Errorf("goal %s does not exist in this room", goal)
		}
		it, found, err := readItem(w.conn, room, goal, item)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("item %s does not exist on goal %s", item, goal)
		}
		if it.State == state {
			out = it
			return nil
		}
		now := s.now()
		it.State, it.UpdatedAt = state, now
		if err := writeItem(w.conn, room, goal, it); err != nil {
			return err
		}
		if g := st.Room(room).Goals[goal]; g != nil {
			g.UpdatedAt = now
		}
		verb := "reopened"
		if state != ItemOpen {
			verb = state
		}
		ev = Event{Goal: goal, Item: item, By: by, Kind: "event", Event: verb, At: now}
		out = it
		return nil
	}, where, args...)
	if err != nil {
		return Item{}, err
	}
	if ev.Goal == "" {
		return out, nil
	}
	_, err = s.AddEvent(room, ev)
	return out, err
}

// EditItem changes an item's text and, when assignee is non-nil, its
// assignee. A nil assignee keeps it; an empty one clears it.
func (s *Store) EditItem(room, goal, item, by, text string, assignee *string) (Item, error) {
	var out Item
	if text == "" && assignee == nil {
		return out, fmt.Errorf("supply new text or --for")
	}
	text = strings.TrimSpace(text)
	if text != "" {
		if err := checkItemText(text); err != nil {
			return out, err
		}
	}
	var ev Event
	where, args := named(room, goal)
	err := s.updateTx(func(w *wake, st *State) error {
		if st.Room(room).Goals[goal] == nil {
			return fmt.Errorf("goal %s does not exist in this room", goal)
		}
		it, found, err := readItem(w.conn, room, goal, item)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("item %s does not exist on goal %s", item, goal)
		}
		if assignee != nil && *assignee != "" && *assignee != it.Assignee {
			if err := w.activate(*assignee); err != nil {
				return fmt.Errorf("cannot assign item %s to %s: %w", item, *assignee, err)
			}
		}
		now := s.now()
		if text != "" {
			it.Text = text
		}
		if assignee != nil {
			it.Assignee = *assignee
		}
		it.UpdatedAt = now
		if err := writeItem(w.conn, room, goal, it); err != nil {
			return err
		}
		if g := st.Room(room).Goals[goal]; g != nil {
			g.UpdatedAt = now
		}
		kind := "edited"
		if assignee != nil && *assignee != "" {
			kind = "assigned"
		}
		ev = Event{Goal: goal, Item: item, By: by, Kind: "event", Event: kind, At: now}
		out = it
		return nil
	}, where, args...)
	if err != nil {
		return Item{}, err
	}
	if ev.Goal == "" {
		return out, nil
	}
	_, err = s.AddEvent(room, ev)
	return out, err
}

// Items lists a goal's items in suffix order.
func (s *Store) Items(room, goal string) ([]Item, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	out := []Item{}
	err = scanItems(db, "SELECT id,text,state,assignee,updated_at FROM goal_items WHERE room=? AND goal=?"+itemOrder, func(it Item) error {
		out = append(out, it)
		return nil
	}, room, goal)
	return out, err
}

// ItemCounts reports each goal's open and total item counts in one query.
func (s *Store) ItemCounts(room string) (map[string][2]int, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT goal, SUM(state='open'), COUNT(*) FROM goal_items WHERE room=? GROUP BY goal", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int{}
	for rows.Next() {
		var goal string
		var open, total int
		if err := rows.Scan(&goal, &open, &total); err != nil {
			return nil, err
		}
		out[goal] = [2]int{open, total}
	}
	return out, rows.Err()
}

// readItem loads one item row inside a writer transaction.
func readItem(conn *sql.Conn, room, goal, item string) (Item, bool, error) {
	var it Item
	var updated int64
	row := conn.QueryRowContext(context.Background(), "SELECT id,text,state,assignee,updated_at FROM goal_items WHERE room=? AND goal=? AND id=?", room, goal, item)
	err := row.Scan(&it.ID, &it.Text, &it.State, &it.Assignee, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	it.UpdatedAt = time.UnixMilli(updated)
	return it, true, nil
}

// writeItem upserts one item row inside a writer transaction.
func writeItem(conn *sql.Conn, room, goal string, it Item) error {
	_, err := conn.ExecContext(context.Background(), "INSERT INTO goal_items(room,goal,id,text,state,assignee,updated_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(room,goal,id) DO UPDATE SET text=excluded.text,state=excluded.state,assignee=excluded.assignee,updated_at=excluded.updated_at", room, goal, it.ID, it.Text, it.State, it.Assignee, it.UpdatedAt.UnixMilli())
	return err
}

// scanItems runs an item query and calls each for every row.
func scanItems(query reader, sqlText string, each func(Item) error, args ...any) error {
	result, err := query.QueryContext(context.Background(), sqlText, args...)
	if err != nil {
		return err
	}
	defer result.Close()
	for result.Next() {
		var it Item
		var updated int64
		if err := result.Scan(&it.ID, &it.Text, &it.State, &it.Assignee, &updated); err != nil {
			return err
		}
		it.UpdatedAt = time.UnixMilli(updated)
		if err := each(it); err != nil {
			return err
		}
	}
	return result.Err()
}

// EffectiveGoal picks the goal a session delegates from: an explicit goal
// ID first, then the goal of the item it is assigned, then its single held
// goal set by someone else. It fails when the choice is ambiguous.
func (s *Store) EffectiveGoal(room, session, explicit string) (string, error) {
	if explicit != "" {
		if _, err := s.Get(room, explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}
	db, err := s.open()
	if err != nil {
		return "", err
	}
	var assigned []string
	rows, err := db.Query("SELECT DISTINCT i.goal FROM goal_items i JOIN goals g ON g.room=i.room AND g.id=i.goal WHERE i.room=? AND i.assignee=? AND i.state='open' AND g.state='active' ORDER BY i.goal", room, session)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var goal string
		if err := rows.Scan(&goal); err != nil {
			rows.Close()
			return "", err
		}
		assigned = append(assigned, goal)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(assigned) == 1 {
		return assigned[0], nil
	}
	if len(assigned) > 1 {
		return "", fmt.Errorf("item assignment spans goals %s; pass --under with the goal to delegate from", strings.Join(assigned, ", "))
	}
	held, err := s.OpenHeldBy(room, session)
	if err != nil {
		return "", err
	}
	held = slices.DeleteFunc(held, func(g Goal) bool { return g.Setter == session })
	if len(held) == 1 {
		return held[0].ID, nil
	}
	if len(held) > 1 {
		ids := make([]string, len(held))
		for i, g := range held {
			ids[i] = g.ID
		}
		return "", fmt.Errorf("goal choice is ambiguous between %s; pass --under with the goal to delegate from", strings.Join(ids, ", "))
	}
	return "", nil
}

// An AssignedItem pairs an open item with its goal.
type AssignedItem struct {
	Goal string
	Item Item
}

// OpenItemsAssignedTo lists a session's open items, suffix order per goal.
func (s *Store) OpenItemsAssignedTo(room, session string) ([]AssignedItem, error) {
	return s.openItems("room=? AND assignee=? AND state='open'", room, session)
}

// OpenItemsIn lists every open item that is assigned to someone in the room,
// suffix order per goal, so a caller that counts a whole room's sessions reads
// the table once. OpenItemsAssignedTo filters this list.
func (s *Store) OpenItemsIn(room string) ([]AssignedItem, error) {
	return s.openItems("room=? AND state='open' AND assignee<>''", room)
}

// openItems reads the open items that the WHERE clause selects.
func (s *Store) openItems(where string, args ...any) ([]AssignedItem, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT goal,id,text,state,assignee,updated_at FROM goal_items WHERE "+where+" ORDER BY goal, CAST(SUBSTR(id,2) AS INTEGER)", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssignedItem{}
	for rows.Next() {
		var a AssignedItem
		var updated int64
		if err := rows.Scan(&a.Goal, &a.Item.ID, &a.Item.Text, &a.Item.State, &a.Item.Assignee, &updated); err != nil {
			return nil, err
		}
		a.Item.UpdatedAt = time.UnixMilli(updated)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReturnItems clears the assignee on a session's open items and journals a
// returned event for each. It reports how many items returned.
func (s *Store) ReturnItems(room, session string) (int, error) {
	var events []Event
	err := s.updateTx(func(w *wake, st *State) error {
		found, err := assignedPairs(w.conn, room, session)
		if err != nil {
			return err
		}
		now := s.now()
		for _, p := range found {
			if err := clearItemAssignee(w.conn, room, p[0], p[1], now); err != nil {
				return err
			}
			if g := st.Room(room).Goals[p[0]]; g != nil {
				g.UpdatedAt = now
			}
			events = append(events, Event{Goal: p[0], Item: p[1], By: session, Kind: "event", Event: "returned", At: now})
		}
		return nil
	}, openGoals)
	if err != nil {
		return 0, err
	}
	for _, ev := range events {
		if _, err := s.AddEvent(room, ev); err != nil {
			return len(events), err
		}
	}
	return len(events), nil
}

// MoveItems moves every item of fromGoal to toGoal with fresh IDs, keeping
// state and assignee. It returns the old-to-new item ID map.
func (s *Store) MoveItems(room, fromGoal, toGoal string) (map[string]string, error) {
	out := map[string]string{}
	err := s.updateTx(func(w *wake, st *State) error {
		g := st.Room(room).Goals[toGoal]
		if g == nil {
			return fmt.Errorf("goal %s does not exist in this room", toGoal)
		}
		if !g.Open() {
			return fmt.Errorf("goal %s is not open", toGoal)
		}
		items, err := s.itemsOn(w.conn, room, fromGoal)
		if err != nil {
			return err
		}
		now := s.now()
		for _, it := range items {
			g.NextItem++
			old := it.ID
			it.ID, it.UpdatedAt = fmt.Sprintf("i%d", g.NextItem), now
			if err := moveItemRow(w.conn, room, fromGoal, old, toGoal, it.ID, now); err != nil {
				return err
			}
			out[old] = it.ID
		}
		if len(out) > 0 {
			g.UpdatedAt = now
		}
		return nil
	}, openGoals)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// itemsOn reads a goal's item ids and states on a writer connection.
func (s *Store) itemsOn(conn *sql.Conn, room, goal string) ([]Item, error) {
	var out []Item
	err := scanItems(conn, "SELECT id,text,state,assignee,updated_at FROM goal_items WHERE room=? AND goal=?"+itemOrder, func(it Item) error {
		out = append(out, it)
		return nil
	}, room, goal)
	return out, err
}

// ReassignItems switches every open item assignee from one session to
// another.
func (s *Store) ReassignItems(room, from, to string) error {
	activated := false
	return s.updateTx(func(w *wake, st *State) error {
		n, err := reassignItemAssignees(w.conn, room, from, to)
		if err != nil || n == 0 {
			return err
		}
		if !activated {
			activated = true
			if err := w.activate(to); err != nil {
				return fmt.Errorf("cannot move items to %s: %w", to, err)
			}
		}
		return nil
	}, openGoals)
}

// assignedPairs reads a session's open item coordinates on a writer conn.
func assignedPairs(conn *sql.Conn, room, session string) ([][2]string, error) {
	rows, err := conn.QueryContext(context.Background(), "SELECT goal,id FROM goal_items WHERE room=? AND assignee=? AND state='open'", room, session)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// clearItemAssignee returns one item to its goal.
func clearItemAssignee(conn *sql.Conn, room, goal, item string, now time.Time) error {
	_, err := conn.ExecContext(context.Background(), "UPDATE goal_items SET assignee='', updated_at=? WHERE room=? AND goal=? AND id=?", now.UnixMilli(), room, goal, item)
	return err
}

// moveItemRow moves one item row to another goal under a fresh ID.
func moveItemRow(conn *sql.Conn, room, fromGoal, oldItem, toGoal, newItem string, now time.Time) error {
	_, err := conn.ExecContext(context.Background(), "UPDATE goal_items SET goal=?, id=?, updated_at=? WHERE room=? AND goal=? AND id=?", toGoal, newItem, now.UnixMilli(), room, fromGoal, oldItem)
	return err
}

// reassignItemAssignees switches open items from one session to another and
// reports how many rows changed.
func reassignItemAssignees(conn *sql.Conn, room, from, to string) (int64, error) {
	result, err := conn.ExecContext(context.Background(), "UPDATE goal_items SET assignee=? WHERE room=? AND assignee=? AND state='open'", to, room, from)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// EventsPage returns a goal's events newest first with a cursor, like
// Journal for entries. A goal of "" returns every goal's events.
func (s *Store) EventsPage(room, goal, before string, limit int) ([]Event, bool, error) {
	out := []Event{}
	path, err := s.journalPath(room)
	if err != nil {
		return out, false, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, false, err
	}
	older, err := readBackLines(f, info.Size(), "e", before, limit, func(line []byte) bool {
		var e Event
		if json.Unmarshal(line, &e) != nil || e.Kind != "event" {
			return false
		}
		if goal != "" && e.Goal != goal {
			return false
		}
		out = append(out, e)
		return true
	})
	return out, older, err
}
