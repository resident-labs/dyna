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
const goalJournalLimit = 50

// GoalRefusal is a request the caller can fix, with the fix in its message.
type GoalRefusal struct{ Message string }

func (e *GoalRefusal) Error() string { return e.Message }

// goalNumber reads the n of "g<n>".
func goalNumber(id contract.GoalID) (int64, error) {
	digits, ok := strings.CutPrefix(string(id), "g")
	if ok {
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, sql.ErrNoRows
}

func goalID(number int64) contract.GoalID { return contract.GoalID(fmt.Sprintf("g%d", number)) }

// goalReplayRef keeps the room beside the number, since goal numbers restart in each room.
func goalReplayRef(room contract.RoomID, id contract.GoalID) string {
	return string(room) + "/" + string(id)
}

// openGoal returns a goal's holder, after checking that the caller may work on it and that it is still active.
func openGoal(ctx context.Context, tx *sql.Tx, tree room.Tree, caller contract.Principal, id contract.RoomID, goal contract.GoalID) (int64, contract.SessionID, error) {
	number, err := goalNumber(goal)
	if err != nil {
		return 0, "", err
	}
	var holder contract.SessionID
	var state contract.GoalState
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(session,''),state FROM goals WHERE room=? AND number=?", id, number).Scan(&holder, &state); err != nil {
		return 0, "", err
	}
	if !tree.CanTend(caller, holder) {
		return 0, "", ErrTreeForbidden
	}
	if state != contract.GoalActive {
		return 0, "", &GoalRefusal{Message: fmt.Sprintf("goal %s is %s; set a new goal instead", goal, state)}
	}
	return number, holder, nil
}

// checkHolder refuses a holder the caller cannot give work to, or one that cannot take it.
func checkHolder(ctx context.Context, tx *sql.Tx, tree room.Tree, caller contract.Principal, holder contract.SessionID) error {
	if holder == tree.Root() && holder != "" {
		return &GoalRefusal{Message: "root holds no goals; name the session that will do the work"}
	}
	if !tree.HoldsGoals(holder) {
		return sql.ErrNoRows
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

// SetGoalItem adds an item to a goal, or changes one.
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
		body := contract.GoalItemChanged{Goal: req.Goal, Item: item, Text: req.Text, State: req.State}
		if item == 0 {
			if req.Text == "" {
				return &GoalRefusal{Message: "a new item needs text"}
			}
			if body.State == "" {
				body.State = contract.GoalItemOpen
			}
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM goal_items WHERE room=? AND goal=?", event.Room, goal).Scan(&item); err != nil {
				return err
			}
			body.Item = item
			if _, err := tx.ExecContext(ctx, "INSERT INTO goal_items(room,goal,number,text,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", event.Room, goal, item, body.Text, body.State, at, at); err != nil {
				return err
			}
		} else if err := tx.QueryRowContext(ctx, "UPDATE goal_items SET text=COALESCE(NULLIF(?,''),text),state=COALESCE(NULLIF(?,''),state),updated_at=? WHERE room=? AND goal=? AND number=? RETURNING text,state", req.Text, req.State, at, event.Room, goal, item).Scan(&body.Text, &body.State); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET updated_at=? WHERE room=? AND number=?", at, event.Room, goal); err != nil {
			return err
		}
		event.Body = body
		_, err = s.AppendEvent(tx, event)
		return err
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
			if open > 0 {
				return &GoalRefusal{Message: fmt.Sprintf("goal %s has %d open items; finish or drop them first", req.Goal, open)}
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

// GoalJournal returns the newest entries of one goal.
func (s *Store) GoalJournal(ctx context.Context, room contract.RoomID, goal contract.GoalID) ([]contract.GoalJournalEntry, error) {
	number, err := goalNumber(goal)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goals WHERE room=? AND number=?)", room, number).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	rows, err := s.Read().QueryContext(ctx, "SELECT at,by_kind,by_id,text FROM goal_journal WHERE room=? AND goal=? ORDER BY number DESC LIMIT ?", room, number, goalJournalLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []contract.GoalJournalEntry{}
	for rows.Next() {
		var entry contract.GoalJournalEntry
		var at int64
		if err := rows.Scan(&at, &entry.By.Kind, &entry.By.Session, &entry.Text); err != nil {
			return nil, err
		}
		entry.At = time.UnixMilli(at).UTC()
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ListGoals returns a room's goals with their items, active ones first and the newest of each kind first.
func (s *Store) ListGoals(ctx context.Context, room contract.RoomID, req contract.GoalListRequest) ([]contract.Goal, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT number,COALESCE(session,''),text,done_when,state,summary,created_at,updated_at FROM goals
		WHERE room=?1 AND (?2 OR state='active') AND (?3='' OR session=?3) ORDER BY state='active' DESC, number DESC LIMIT ?4`, room, req.All, req.Session, goalListLimit)
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
		items, err := s.Read().QueryContext(ctx, "SELECT number,text,state FROM goal_items WHERE room=? AND goal=? ORDER BY number", room, number)
		if err != nil {
			return nil, err
		}
		for items.Next() {
			var item contract.GoalItem
			if err := items.Scan(&item.Number, &item.Text, &item.State); err != nil {
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

// transferGoals hands a closing session's open goals to its parent. Root holds no goals, so under root they go unheld.
func (s *Store) transferGoals(ctx context.Context, tx *sql.Tx, session, parent contract.SessionID, at time.Time) ([]contract.GoalID, error) {
	var parentIsRoot bool
	if err := tx.QueryRowContext(ctx, "SELECT parent IS NULL FROM sessions WHERE id=?", parent).Scan(&parentIsRoot); err != nil {
		return nil, err
	}
	if !parentIsRoot {
		_, err := tx.ExecContext(ctx, "UPDATE goals SET session=?,updated_at=? WHERE session=? AND state='active'", parent, at.UnixMilli(), session)
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "UPDATE goals SET session=NULL,updated_at=? WHERE session=? AND state='active' RETURNING number", at.UnixMilli(), session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var unheld []contract.GoalID
	for rows.Next() {
		var number int64
		if err := rows.Scan(&number); err != nil {
			return nil, err
		}
		unheld = append(unheld, goalID(number))
	}
	return unheld, rows.Err()
}
