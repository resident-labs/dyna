package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// goalListLimit bounds a listing that includes ended goals. Active goals sort first.
const goalListLimit = 200

// goalJournalLimit is how many entries a journal read returns.
const goalJournalLimit = 5

// GoalRefusal is a request the caller can fix, with the fix in its message. Missing marks a request that names something absent.
type GoalRefusal struct {
	Message string
	Missing bool
}

func (e *GoalRefusal) Error() string { return e.Message }

// goalMissing refuses a goal the room does not hold.
func goalMissing(goal contract.GoalID) *GoalRefusal {
	if goal == "" {
		return &GoalRefusal{Message: "goal is required; goal list shows the room's goals"}
	}
	return &GoalRefusal{Message: fmt.Sprintf("goal %s not found; goal list shows the room's goals", goal), Missing: true}
}

// goalNumber reads the n of "g<n>".
func goalNumber(id contract.GoalID) (int64, error) {
	digits, ok := strings.CutPrefix(string(id), "g")
	if ok {
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, goalMissing(id)
}

func goalID(number int64) contract.GoalID { return contract.GoalID(fmt.Sprintf("g%d", number)) }

// goalReplayRef keeps the room beside the number, since goal numbers restart in each room.
func goalReplayRef(room contract.RoomID, id contract.GoalID) string {
	return string(room) + "/" + string(id)
}

// heldGoal returns the active goal session holds other than except, with its text, or 0 when it holds none.
func heldGoal(ctx context.Context, tx *sql.Tx, room contract.RoomID, session contract.SessionID, except int64) (int64, string, error) {
	var number int64
	var text string
	err := tx.QueryRowContext(ctx, "SELECT number,text FROM goals WHERE room=? AND session=? AND state='active' AND number<>? ORDER BY number LIMIT 1", room, session, except).Scan(&number, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return number, text, err
}

// refuseSecondGoal refuses giving holder a goal while it holds another: a session holds one goal, and more work goes in as items.
func refuseSecondGoal(ctx context.Context, tx *sql.Tx, room contract.RoomID, holder contract.SessionID, except int64) error {
	held, text, err := heldGoal(ctx, tx, room, holder, except)
	if err != nil || held == 0 {
		return err
	}
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM sessions WHERE id=?", holder).Scan(&name); err != nil {
		return err
	}
	if runes := []rune(text); len(runes) > 80 {
		text = string(runes[:80]) + "…"
	}
	return &GoalRefusal{Message: fmt.Sprintf("session %s already holds %s (%q), and a session holds one goal; add this work to it as an item (goal item with goal %s and text, and parent for a subtask), change it with goal set and goal, or end it with goal done first", name, goalID(held), text, goalID(held))}
}

// openGoal returns a goal's holder, after checking that the caller may work on it and that it is still active.
func openGoal(ctx context.Context, tx *sql.Tx, tree room.Tree, caller contract.Principal, id contract.RoomID, goal contract.GoalID) (int64, contract.SessionID, error) {
	number, err := goalNumber(goal)
	if err != nil {
		return 0, "", err
	}
	var holder contract.SessionID
	var state contract.GoalState
	var foldedGoal, foldedItem int64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(session,''),state,folded_goal,folded_item FROM goals WHERE room=? AND number=?", id, number).Scan(&holder, &state, &foldedGoal, &foldedItem)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", goalMissing(goal)
	}
	if err != nil {
		return 0, "", err
	}
	// An ended goal says where its work went before the tree check: its holder may have closed, and the work moved on.
	if state != contract.GoalActive {
		if foldedGoal > 0 {
			return 0, "", &GoalRefusal{Message: fmt.Sprintf("goal %s was folded into %s as item %d when its session closed; work on that item of %s", goal, goalID(foldedGoal), foldedItem, goalID(foldedGoal))}
		}
		if caller.Kind == contract.PrincipalSession {
			if held, _, err := heldGoal(ctx, tx, id, caller.Session, 0); err != nil {
				return 0, "", err
			} else if held > 0 {
				return 0, "", &GoalRefusal{Message: fmt.Sprintf("goal %s is %s; your goal is %s, so add further work to it as an item with goal item", goal, state, goalID(held))}
			}
		}
		return 0, "", &GoalRefusal{Message: fmt.Sprintf("goal %s is %s; set a new goal instead", goal, state)}
	}
	if !tree.CanTend(caller, holder) {
		return 0, "", ErrTreeForbidden
	}
	return number, holder, nil
}

// checkHolder refuses a holder the caller cannot give work to, or one that cannot take it.
func checkHolder(ctx context.Context, tx *sql.Tx, tree room.Tree, caller contract.Principal, holder contract.SessionID) error {
	if holder == tree.Root() && holder != "" {
		return &GoalRefusal{Message: "root holds no goals; name the session that will do the work"}
	}
	if !tree.HoldsGoals(holder) {
		return &GoalRefusal{Message: fmt.Sprintf("session %s not found in this room; session list shows them", holder), Missing: true}
	}
	if !tree.CanTend(caller, holder) {
		return ErrTreeForbidden
	}
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", holder).Scan(&state); err != nil {
		return err
	}
	if state == "closing" {
		return ErrSessionClosing
	}
	return nil
}

// SetGoal creates a goal or changes one. A holder that someone else gave the goal to wakes on it, as on a brief. event carries
// the caller, the time and the room; the write fills its body.
func (s *Store) SetGoal(ctx context.Context, req contract.GoalSetRequest, event contract.Event) (contract.GoalID, error) {
	var id contract.GoalID
	caller := event.From
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if req.ClientID != "" {
			var ref string
			err := tx.QueryRowContext(ctx, "SELECT ref FROM client_replays WHERE method=? AND caller_kind=? AND caller_id=? AND client_id=?", contract.MethodGoalSet, caller.Kind, caller.Session, req.ClientID).Scan(&ref)
			if err == nil {
				if _, goal, ok := strings.Cut(ref, "/"); ok {
					id = contract.GoalID(goal)
				}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := refuseNewWork(ctx, tx, event.Room); err != nil {
			return err
		}
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		body := contract.GoalSet{Goal: req.Goal, Session: req.Session, Text: req.Text, DoneWhen: req.DoneWhen}
		var previous contract.SessionID
		at := event.Time.UnixMilli()
		if req.Goal == "" {
			if req.Text == "" || req.DoneWhen == "" {
				return &GoalRefusal{Message: "a new goal needs text and doneWhen"}
			}
			if body.Session == "" {
				body.Session = caller.Session
			}
			if body.Session == "" {
				return &GoalRefusal{Message: "name the session that holds the goal"}
			}
			if err := checkHolder(ctx, tx, tree, caller, body.Session); err != nil {
				return err
			}
			if err := refuseSecondGoal(ctx, tx, event.Room, body.Session, 0); err != nil {
				return err
			}
			var number int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM goals WHERE room=?", event.Room).Scan(&number); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", event.Room, number, body.Session, body.Text, body.DoneWhen, contract.GoalActive, at, at); err != nil {
				return err
			}
			body.Goal = goalID(number)
			if req.ClientID != "" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES(?,?,?,?,?,?)", contract.MethodGoalSet, caller.Kind, caller.Session, req.ClientID, goalReplayRef(event.Room, body.Goal), at); err != nil {
					return err
				}
			}
		} else {
			number, holder, err := openGoal(ctx, tx, tree, caller, event.Room, req.Goal)
			if err != nil {
				return err
			}
			previous = holder
			if body.Session == "" {
				body.Session = holder
			} else if body.Session != holder {
				if err := checkHolder(ctx, tx, tree, caller, body.Session); err != nil {
					return err
				}
				if err := refuseSecondGoal(ctx, tx, event.Room, body.Session, number); err != nil {
					return err
				}
			}
			if err := tx.QueryRowContext(ctx, "UPDATE goals SET session=NULLIF(?,''),text=COALESCE(NULLIF(?,''),text),done_when=COALESCE(NULLIF(?,''),done_when),updated_at=? WHERE room=? AND number=? RETURNING text,done_when", body.Session, req.Text, req.DoneWhen, at, event.Room, number).Scan(&body.Text, &body.DoneWhen); err != nil {
				return err
			}
		}
		event.Body = body
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		id = body.Goal
		if body.Session != previous && (caller.Kind != contract.PrincipalSession || caller.Session != body.Session) {
			reply := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: caller, Home: contract.Conversation{Kind: contract.ConversationFeed}}
			return s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: body.Session}, Surface: contract.SurfaceSession, Reply: reply, Wake: true})
		}
		return nil
	})
	return id, err
}

// SetGoalItem adds an item to a goal, under a parent item for a subtask, or changes one. Items go two levels deep, and an
// open item never sits under a settled one: a done item needs its subtasks settled, dropping an item drops its open
// subtasks, and opening a subtask or adding one reopens its parent. Each item that changes gets its own event.
func (s *Store) SetGoalItem(ctx context.Context, req contract.GoalItemRequest, event contract.Event) (int, error) {
	item := req.Item
	err := s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		goal, _, err := openGoal(ctx, tx, tree, event.From, event.Room, req.Goal)
		if err != nil {
			return err
		}
		at := event.Time.UnixMilli()
		var changed []contract.GoalItemChanged
		// reopen opens a settled parent before an open item goes under it.
		reopen := func(parent int) error {
			var text string
			err := tx.QueryRowContext(ctx, "UPDATE goal_items SET state=?,updated_at=? WHERE room=? AND goal=? AND number=? AND state<>? RETURNING text", contract.GoalItemOpen, at, event.Room, goal, parent, contract.GoalItemOpen).Scan(&text)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err == nil {
				changed = append(changed, contract.GoalItemChanged{Goal: req.Goal, Item: parent, Text: text, State: contract.GoalItemOpen})
			}
			return err
		}
		if item == 0 {
			if req.Text == "" {
				return &GoalRefusal{Message: "a new item needs text"}
			}
			body := contract.GoalItemChanged{Goal: req.Goal, Parent: req.Parent, Text: req.Text, State: req.State}
			if body.State == "" {
				body.State = contract.GoalItemOpen
			}
			if req.Parent != 0 {
				var grand int
				err := tx.QueryRowContext(ctx, "SELECT parent FROM goal_items WHERE room=? AND goal=? AND number=?", event.Room, goal, req.Parent).Scan(&grand)
				if errors.Is(err, sql.ErrNoRows) {
					return &GoalRefusal{Message: fmt.Sprintf("goal %s has no item %d to put a subtask under; goal list shows its items", req.Goal, req.Parent), Missing: true}
				}
				if err != nil {
					return err
				}
				if grand != 0 {
					return &GoalRefusal{Message: fmt.Sprintf("item %d is itself a subtask of item %d, and subtasks go one level deep; add this under item %d", req.Parent, grand, grand)}
				}
				if body.State == contract.GoalItemOpen {
					if err := reopen(req.Parent); err != nil {
						return err
					}
				}
			}
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM goal_items WHERE room=? AND goal=?", event.Room, goal).Scan(&item); err != nil {
				return err
			}
			body.Item = item
			if _, err := tx.ExecContext(ctx, "INSERT INTO goal_items(room,goal,number,parent,text,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", event.Room, goal, item, body.Parent, body.Text, body.State, at, at); err != nil {
				return err
			}
			changed = append(changed, body)
		} else {
			if req.Parent != 0 {
				return &GoalRefusal{Message: "parent applies only when adding an item; an item keeps the parent it was added under"}
			}
			var parent int
			var state contract.GoalItemState
			err := tx.QueryRowContext(ctx, "SELECT parent,state FROM goal_items WHERE room=? AND goal=? AND number=?", event.Room, goal, item).Scan(&parent, &state)
			if errors.Is(err, sql.ErrNoRows) {
				return &GoalRefusal{Message: fmt.Sprintf("goal %s has no item %d; goal list shows its items", req.Goal, item), Missing: true}
			}
			if err != nil {
				return err
			}
			switch req.State {
			case contract.GoalItemDone:
				var open string
				if err := tx.QueryRowContext(ctx, "SELECT COALESCE(group_concat(number, ', '),'') FROM goal_items WHERE room=? AND goal=? AND parent=? AND state=?", event.Room, goal, item, contract.GoalItemOpen).Scan(&open); err != nil {
					return err
				}
				if open != "" {
					return &GoalRefusal{Message: fmt.Sprintf("item %d has open subtasks (%s); set each to done or dropped first, or drop item %d to drop them with it", item, open, item)}
				}
			case contract.GoalItemDropped:
				rows, err := tx.QueryContext(ctx, "UPDATE goal_items SET state=?,updated_at=? WHERE room=? AND goal=? AND parent=? AND state=? RETURNING number,text", contract.GoalItemDropped, at, event.Room, goal, item, contract.GoalItemOpen)
				if err != nil {
					return err
				}
				for rows.Next() {
					sub := contract.GoalItemChanged{Goal: req.Goal, Parent: item, State: contract.GoalItemDropped}
					if err := rows.Scan(&sub.Item, &sub.Text); err != nil {
						rows.Close()
						return err
					}
					changed = append(changed, sub)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
			case contract.GoalItemOpen:
				if parent != 0 {
					if err := reopen(parent); err != nil {
						return err
					}
				}
			}
			body := contract.GoalItemChanged{Goal: req.Goal, Item: item, Parent: parent}
			if err := tx.QueryRowContext(ctx, "UPDATE goal_items SET text=COALESCE(NULLIF(?,''),text),state=COALESCE(NULLIF(?,''),state),updated_at=? WHERE room=? AND goal=? AND number=? RETURNING text,state", req.Text, req.State, at, event.Room, goal, item).Scan(&body.Text, &body.State); err != nil {
				return err
			}
			changed = append(changed, body)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET updated_at=? WHERE room=? AND number=?", at, event.Room, goal); err != nil {
			return err
		}
		for i, body := range changed {
			each := event
			if i > 0 {
				each.ID = contract.EventID(fmt.Sprintf("%s_%d", event.ID, i))
			}
			each.Body = body
			if _, err := s.AppendEvent(tx, each); err != nil {
				return err
			}
		}
		return nil
	})
	return item, err
}

// EndGoal ends a goal. Done needs every item settled; dropped leaves them as they are.
func (s *Store) EndGoal(ctx context.Context, req contract.GoalDoneRequest, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		goal, holder, err := openGoal(ctx, tx, tree, event.From, event.Room, req.Goal)
		if err != nil {
			return err
		}
		state := contract.GoalDropped
		if !req.Drop {
			state = contract.GoalDone
			var open int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM goal_items WHERE room=? AND goal=? AND state=?", event.Room, goal, contract.GoalItemOpen).Scan(&open); err != nil {
				return err
			}
			if open > 0 && holder == "" {
				// Nobody holds the goal to settle its items, so ending it settles them.
				if _, err := tx.ExecContext(ctx, "UPDATE goal_items SET state=?,updated_at=? WHERE room=? AND goal=? AND state=?", contract.GoalItemDropped, event.Time.UnixMilli(), event.Room, goal, contract.GoalItemOpen); err != nil {
					return err
				}
			} else if open > 0 {
				var numbers string
				if err := tx.QueryRowContext(ctx, "SELECT group_concat(number, ', ') FROM goal_items WHERE room=? AND goal=? AND state=?", event.Room, goal, contract.GoalItemOpen).Scan(&numbers); err != nil {
					return err
				}
				return &GoalRefusal{Message: fmt.Sprintf("goal %s has %d open items (%s); set each to done or dropped with goal item (item and state), then end the goal, or end it with drop to give it up", req.Goal, open, numbers)}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET state=?,summary=?,updated_at=? WHERE room=? AND number=?", state, req.Summary, event.Time.UnixMilli(), event.Room, goal); err != nil {
			return err
		}
		event.Body = contract.GoalEnded{Goal: req.Goal, Session: holder, State: state, Summary: req.Summary}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

// AddGoalJournal adds a line to an active goal's journal. The line counts as progress on the goal.
func (s *Store) AddGoalJournal(ctx context.Context, room contract.RoomID, goal contract.GoalID, by contract.Principal, text string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, room)
		if err != nil {
			return err
		}
		number, _, err := openGoal(ctx, tx, tree, by, room, goal)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO goal_journal(room,goal,number,at,by_kind,by_id,text) SELECT ?,?,COALESCE(MAX(number),0)+1,?,?,?,? FROM goal_journal WHERE room=? AND goal=?", room, number, at.UnixMilli(), by.Kind, by.Session, text, room, number); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE goals SET updated_at=? WHERE room=? AND number=?", at.UnixMilli(), room, number)
		return err
	})
}

// GoalJournal returns the newest entries of one goal, and how many older ones there are.
func (s *Store) GoalJournal(ctx context.Context, room contract.RoomID, goal contract.GoalID) (entries []contract.GoalJournalEntry, older int, err error) {
	number, err := goalNumber(goal)
	if err != nil {
		return nil, 0, err
	}
	var exists bool
	if err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goals WHERE room=? AND number=?)", room, number).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, goalMissing(goal)
	}
	rows, err := s.Read().QueryContext(ctx, "SELECT at,by_kind,by_id,text,(SELECT COUNT(*) FROM goal_journal WHERE room=?1 AND goal=?2) FROM goal_journal WHERE room=?1 AND goal=?2 ORDER BY number DESC LIMIT ?3", room, number, goalJournalLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries = []contract.GoalJournalEntry{}
	for rows.Next() {
		var entry contract.GoalJournalEntry
		var at int64
		var total int
		if err := rows.Scan(&at, &entry.By.Kind, &entry.By.Session, &entry.Text, &total); err != nil {
			return nil, 0, err
		}
		entry.At = time.UnixMilli(at).UTC()
		entries = append(entries, entry)
		older = total - len(entries)
	}
	return entries, older, rows.Err()
}

// ListGoals returns a room's goals with their items, active ones first and the newest of each kind first. Root holds no goals,
// so listing root's shows the goals that have no holder.
func (s *Store) ListGoals(ctx context.Context, room contract.RoomID, req contract.GoalListRequest) ([]contract.Goal, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT number,COALESCE(session,''),text,done_when,state,summary,created_at,updated_at FROM goals
		WHERE room=?1 AND (?2 OR state='active') AND (?3='' OR session=?3 OR (session IS NULL AND ?3=(SELECT id FROM sessions WHERE room=?1 AND parent IS NULL))) ORDER BY state='active' DESC, number DESC LIMIT ?4`, room, req.All, req.Session, goalListLimit)
	if err != nil {
		return nil, err
	}
	goals := []contract.Goal{}
	for rows.Next() {
		var goal contract.Goal
		var number, created, updated int64
		if err := rows.Scan(&number, &goal.Session, &goal.Text, &goal.DoneWhen, &goal.State, &goal.Summary, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		goal.ID, goal.Room, goal.Items = goalID(number), room, []contract.GoalItem{}
		goal.Created, goal.Updated = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		goals = append(goals, goal)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range goals {
		number, _ := goalNumber(goals[i].ID)
		items, err := s.Read().QueryContext(ctx, "SELECT number,parent,text,state FROM goal_items WHERE room=? AND goal=? ORDER BY number", room, number)
		if err != nil {
			return nil, err
		}
		for items.Next() {
			var item contract.GoalItem
			if err := items.Scan(&item.Number, &item.Parent, &item.Text, &item.State); err != nil {
				items.Close()
				return nil, err
			}
			goals[i].Items = append(goals[i].Items, item)
		}
		err = items.Err()
		items.Close()
		if err != nil {
			return nil, err
		}
	}
	return goals, nil
}

// goalClose is what a session's close did with its active goal. At most one field is set.
type goalClose struct {
	Ended  contract.GoalID // it had no open item and was closed on purpose, so it ended done
	Moved  contract.GoalID // it became the goal of the nearest open ancestor, which held none
	Unheld contract.GoalID // that ancestor is root, which holds no goals
	Folded *contract.GoalFold
	Target contract.SessionID // the ancestor that took the work, or root for an unheld goal
}

// settleGoal ends, moves or folds the active goal of a closing session, in the close's write. deliberate is a close that
// an ancestor or the owner asked for, after its result; a dormant or disk close is the engine's. The goal's work goes to
// the nearest ancestor that is not closing, so a subtree close folds every goal straight into the one that stays.
func (s *Store) settleGoal(ctx context.Context, tx *sql.Tx, event contract.Event, session contract.SessionID, name string, deliberate bool) (goalClose, error) {
	var out goalClose
	var number int64
	var text, doneWhen string
	err := tx.QueryRowContext(ctx, "SELECT number,text,done_when FROM goals WHERE room=? AND session=? AND state='active' ORDER BY number LIMIT 1", event.Room, session).Scan(&number, &text, &doneWhen)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	at := event.Time.UnixMilli()
	ended := func(state contract.GoalState, summary string) error {
		_, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID(fmt.Sprintf("%s_g%d", event.ID, number)), Time: event.Time, Room: event.Room, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.GoalEnded{Goal: goalID(number), Session: session, State: state, Summary: summary}})
		return err
	}
	// Open leaves: open items with no open subtask. They are the work left; an open parent of open subtasks is only a heading.
	leaves, err := tx.QueryContext(ctx, "SELECT text FROM goal_items i WHERE room=?1 AND goal=?2 AND state='open' AND NOT EXISTS (SELECT 1 FROM goal_items c WHERE c.room=?1 AND c.goal=?2 AND c.parent=i.number AND c.state='open') ORDER BY CASE parent WHEN 0 THEN number ELSE parent END, parent<>0, number", event.Room, number)
	if err != nil {
		return out, err
	}
	var open []string
	for leaves.Next() {
		var leaf string
		if err := leaves.Scan(&leaf); err != nil {
			leaves.Close()
			return out, err
		}
		open = append(open, leaf)
	}
	err = leaves.Err()
	leaves.Close()
	if err != nil {
		return out, err
	}
	if len(open) == 0 && deliberate {
		summary := "ended when " + name + " closed"
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET state=?,summary=?,updated_at=? WHERE room=? AND number=?", contract.GoalDone, summary, at, event.Room, number); err != nil {
			return out, err
		}
		out.Ended = goalID(number)
		return out, ended(contract.GoalDone, summary)
	}
	var target contract.SessionID
	var targetIsRoot bool
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE up(id,parent,state,depth) AS (
		SELECT s.id,s.parent,s.state,0 FROM sessions s WHERE s.id=(SELECT parent FROM sessions WHERE id=?)
		UNION ALL SELECT s.id,s.parent,s.state,up.depth+1 FROM sessions s JOIN up ON s.id=up.parent WHERE up.state IN ('closing','closed'))
		SELECT id,parent IS NULL FROM up WHERE state NOT IN ('closing','closed') OR parent IS NULL ORDER BY depth LIMIT 1`, session).Scan(&target, &targetIsRoot)
	out.Target = target
	if errors.Is(err, sql.ErrNoRows) || err == nil && targetIsRoot {
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET session=NULL,updated_at=? WHERE room=? AND number=?", at, event.Room, number); err != nil {
			return out, err
		}
		out.Unheld = goalID(number)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	into, _, err := heldGoal(ctx, tx, event.Room, target, number)
	if err != nil {
		return out, err
	}
	if into == 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET session=?,updated_at=? WHERE room=? AND number=?", target, at, event.Room, number); err != nil {
			return out, err
		}
		out.Moved = goalID(number)
		return out, nil
	}
	var header int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM goal_items WHERE room=? AND goal=?", event.Room, into).Scan(&header); err != nil {
		return out, err
	}
	heading := fmt.Sprintf("from %s, %s: %s", name, goalID(number), text)
	if doneWhen != "" {
		heading += " — done when: " + doneWhen
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO goal_items(room,goal,number,parent,text,state,created_at,updated_at) VALUES(?,?,?,0,?,?,?,?)", event.Room, into, header, heading, contract.GoalItemOpen, at, at); err != nil {
		return out, err
	}
	for i, leaf := range open {
		if _, err := tx.ExecContext(ctx, "INSERT INTO goal_items(room,goal,number,parent,text,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", event.Room, into, header+1+i, header, leaf, contract.GoalItemOpen, at, at); err != nil {
			return out, err
		}
	}
	summary := fmt.Sprintf("folded into %s as item %d when %s closed", goalID(into), header, name)
	if _, err := tx.ExecContext(ctx, "UPDATE goals SET state=?,summary=?,folded_goal=?,folded_item=?,updated_at=? WHERE room=? AND number=?", contract.GoalDropped, summary, into, header, at, event.Room, number); err != nil {
		return out, err
	}
	out.Folded = &contract.GoalFold{Goal: goalID(number), Into: goalID(into), Item: header}
	return out, ended(contract.GoalDropped, summary)
}
