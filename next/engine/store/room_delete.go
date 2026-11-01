package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// ErrRoomDeleting refuses new work in a room on its way out.
var ErrRoomDeleting = errors.New("room is being deleted")

// RoomOpen returns ErrNotFound for an unknown room and ErrRoomDeleting for one being deleted.
func (s *Store) RoomOpen(ctx context.Context, room contract.RoomID) error {
	return roomOpen(ctx, s.Read(), room)
}

func roomOpen(ctx context.Context, q sqlQuery, room contract.RoomID) error {
	var state, reason string
	if err := q.QueryRowContext(ctx, "SELECT state,reason FROM rooms WHERE id=?", room).Scan(&state, &reason); err != nil {
		return err
	}
	if state == "open" {
		return nil
	}
	if reason != "" {
		return fmt.Errorf("%w: %s", ErrRoomDeleting, reason)
	}
	return ErrRoomDeleting
}

// refuseNewWork checks a write against the deletion in the same transaction. An unknown room is left to the foreign keys.
func refuseNewWork(ctx context.Context, tx *sql.Tx, room contract.RoomID) error {
	if err := roomOpen(ctx, tx, room); !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func listIDs[ID ~string](ctx context.Context, q sqlQuery, query string, args ...any) ([]ID, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []ID
	for rows.Next() {
		var id ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OpenRooms lists the rooms that are not being deleted.
func (s *Store) OpenRooms(ctx context.Context) ([]contract.RoomID, error) {
	return listIDs[contract.RoomID](ctx, s.Read(), "SELECT id FROM rooms WHERE state='open' ORDER BY id")
}

// RoomsReadyToDelete lists deleting rooms with no open session and nothing still running.
// Pending operations do not count: the deletion expired them.
func (s *Store) RoomsReadyToDelete(ctx context.Context) ([]contract.RoomID, error) {
	return listIDs[contract.RoomID](ctx, s.Read(), `SELECT r.id FROM rooms r WHERE r.state='deleting'
		AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.room=r.id AND s.closed_at IS NULL)
		AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.room=r.id AND o.state IN ('approved','running'))
		AND NOT EXISTS (SELECT 1 FROM hostexec_runs h JOIN grants g ON g.id=h.grant_id WHERE g.room=r.id AND h.ended_at IS NULL)
		ORDER BY r.id`)
}

// RoomLimitedError refuses a deletion that would widen a limit. An empty limit means every room, so a limit
// must keep an open room when this one goes. A room being deleted does not count, or two deletions could empty one limit.
type RoomLimitedError struct{ Limited contract.RoomLimited }

func (e *RoomLimitedError) Error() string {
	var names []string
	for _, account := range e.Limited.Accounts {
		names = append(names, "account "+string(account))
	}
	for _, host := range e.Limited.Hosts {
		names = append(names, "host "+string(host))
	}
	return "this is the only open room of " + strings.Join(names, ", ") + "; change those limits first"
}

func onlyRoomOf(ctx context.Context, tx *sql.Tx, room contract.RoomID) (contract.RoomLimited, error) {
	var limited contract.RoomLimited
	var err error
	limited.Accounts, err = listIDs[contract.AccountID](ctx, tx, "SELECT l.account FROM account_rooms l WHERE l.room=?1 AND NOT EXISTS (SELECT 1 FROM account_rooms o JOIN rooms r ON r.id=o.room WHERE o.account=l.account AND o.room!=?1 AND r.state='open') ORDER BY l.account", room)
	if err != nil {
		return limited, err
	}
	limited.Hosts, err = listIDs[contract.HostName](ctx, tx, "SELECT l.host FROM host_rooms l WHERE l.room=?1 AND NOT EXISTS (SELECT 1 FROM host_rooms o JOIN rooms r ON r.id=o.room WHERE o.host=l.host AND o.room!=?1 AND r.state='open') ORDER BY l.host", room)
	return limited, err
}

// MarkRoomDeleting records the intent and puts every open session on the close path with discard, in one write.
// The limit check runs in this write, and SetAccountRooms, SetHostRooms and AddHost refuse a deleting room, so no limit can come to depend on the room afterwards.
// Pending operations and archive refs fall due at once, so their own sweeps end them. Those sweeps retry without the room, so an outage cannot hold it.
func (s *Store) MarkRoomDeleting(ctx context.Context, room contract.RoomID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := roomOpen(ctx, tx, room); err != nil {
			return err
		}
		limited, err := onlyRoomOf(ctx, tx, room)
		if err != nil {
			return err
		}
		if len(limited.Accounts)+len(limited.Hosts) > 0 {
			return &RoomLimitedError{Limited: limited}
		}
		now := event.Time.UnixMilli()
		for _, step := range []string{
			"UPDATE rooms SET state='deleting' WHERE id=?1",
			"UPDATE sessions SET state='closing' WHERE room=?1 AND closed_at IS NULL",
			"UPDATE boxes SET want='discard',due_at=0 WHERE want!='discard' AND session IN (SELECT id FROM sessions WHERE room=?1 AND closed_at IS NULL)",
			"UPDATE hostexec_runs SET stopped='closed' WHERE ended_at IS NULL AND grant_id IN (SELECT id FROM grants WHERE room=?1)",
			"UPDATE operations SET expires_at=?2 WHERE room=?1 AND state='pending'",
			"UPDATE archives SET expires_at=?2,retry_at=0 WHERE session IN (SELECT id FROM sessions WHERE room=?1)",
		} {
			if _, err := tx.ExecContext(ctx, step, room, now); err != nil {
				return err
			}
		}
		if err := s.withdrawQuestions(ctx, tx, event.Time); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

// SetRoomDeleteReason shows why a deletion is held.
func (s *Store) SetRoomDeleteReason(ctx context.Context, room contract.RoomID, reason string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE rooms SET reason=? WHERE id=? AND state='deleting'", reason, room)
		return err
	})
}

// deleteRoom removes rows children first, so the foreign keys hold after each statement. A row is reached through its room,
// its session or its event, since a delivery can cross rooms in either direction. Deleting events and sessions fires
// the triggers that clear post_search and session_search. Archive rows that hold a ref stay until the sweep deletes the ref.
var deleteRoom = []string{
	`DELETE FROM hostexec_runs WHERE grant_id IN (SELECT id FROM grants WHERE room=?1) OR op IN (SELECT id FROM operations WHERE room=?1 OR (by_kind='session' AND by_id IN (SELECT id FROM sessions WHERE room=?1)))`,
	`DELETE FROM client_replays WHERE ref IN (SELECT id FROM sessions WHERE room=?1) OR ref IN (SELECT id FROM operations WHERE room=?1) OR ref IN (SELECT id FROM questions WHERE room=?1) OR (caller_kind='session' AND caller_id IN (SELECT id FROM sessions WHERE room=?1)) OR (method='goal.set' AND ref LIKE ?1||'/%')`,
	`DELETE FROM operations WHERE room=?1 OR (by_kind='session' AND by_id IN (SELECT id FROM sessions WHERE room=?1))`,
	`DELETE FROM grants WHERE room=?1`,
	`DELETE FROM questions WHERE room=?1`,
	`DELETE FROM deliveries WHERE seq IN (SELECT seq FROM events WHERE room=?1) OR (to_kind='session' AND to_id IN (SELECT id FROM sessions WHERE room=?1))`,
	`DELETE FROM event_files WHERE seq IN (SELECT seq FROM events WHERE room=?1)`,
	`DELETE FROM events WHERE room=?1`,
	`DELETE FROM visuals WHERE room=?1`,
	`DELETE FROM turns WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM transcript_offsets WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM transcript_turns WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM session_accounts WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM shell_jobs WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM pr_owners WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM goal_journal WHERE room=?1`,
	`DELETE FROM goal_items WHERE room=?1`,
	`DELETE FROM goals WHERE room=?1`,
	`DELETE FROM checkins WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM boxes WHERE session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM archives WHERE ref='' AND session IN (SELECT id FROM sessions WHERE room=?1)`,
	`DELETE FROM sessions WHERE room=?1`,
	`DELETE FROM room_repos WHERE room=?1`,
	// A repository another room lists stays, with a default room that exists. One no room lists goes, and with it any PR a
	// session of another room opened there, since no room watches that repository any more, and its CI runners, as --mode github would.
	`UPDATE repos SET default_room=(SELECT MIN(room) FROM room_repos WHERE repo=repos.repo) WHERE default_room=?1 AND EXISTS (SELECT 1 FROM room_repos WHERE repo=repos.repo)`,
	`DELETE FROM pr_owners WHERE repo IN (SELECT repo FROM repos WHERE default_room=?1)`,
	`DELETE FROM repo_ci_hosts WHERE repo IN (SELECT repo FROM repos WHERE default_room=?1)`,
	`DELETE FROM repos WHERE default_room=?1`,
	`DELETE FROM host_rooms WHERE room=?1`,
	`DELETE FROM room_shared WHERE room=?1`,
	`DELETE FROM account_rooms WHERE room=?1`,
	`DELETE FROM conversation_reads WHERE room=?1`,
	`DELETE FROM rooms WHERE id=?1`,
}

// DeleteRoom removes every row of a room in one transaction. Running it again changes nothing.
func (s *Store) DeleteRoom(ctx context.Context, room contract.RoomID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var open bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sessions WHERE room=? AND closed_at IS NULL)", room).Scan(&open)
		if err != nil {
			return err
		}
		if open {
			return fmt.Errorf("room %s has open sessions", room)
		}
		var state string
		err = tx.QueryRowContext(ctx, "SELECT state FROM rooms WHERE id=?", room).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "deleting" {
			return fmt.Errorf("room %s is not being deleted", room)
		}
		for _, statement := range deleteRoom {
			if _, err := tx.ExecContext(ctx, statement, room); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
}

// Leftover is what a deleted room still owes an offline host: a box's resources, or the room's folder when Box is empty.
type Leftover struct {
	Host  contract.HostName
	Room  contract.RoomID
	Box   contract.BoxID
	Slice string
}

// AbandonBox stops waiting for a closing box on an offline host. The box counts as deleted, so the close goes on,
// and the leftover row keeps the debt: its resources go when the host returns.
func (s *Store) AbandonBox(ctx context.Context, b box.Box, event contract.Event) error {
	_, body, err := box.Transition(b, contract.BoxDeleted, "host unreachable", event.Time)
	if err != nil {
		return err
	}
	body.Host = b.Host
	event.Body = body
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET state=?,reason='',updated_at=? WHERE id=? AND state=?", contract.BoxDeleted, event.Time.UnixMilli(), b.ID, b.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO room_leftovers(host,room,box,slice) VALUES(?,?,?,?)", b.Host, b.Room, b.ID, b.Slice); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

// AddRoomLeftover records that a host offline at deletion still holds the room's folder.
func (s *Store) AddRoomLeftover(ctx context.Context, host contract.HostName, room contract.RoomID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO room_leftovers(host,room,box,slice) VALUES(?,?,'','')", host, room)
		return err
	})
}

// Leftovers lists the debts of the given host, boxes before folders, since a box mounts its room's folder.
func (s *Store) Leftovers(ctx context.Context, host contract.HostName) ([]Leftover, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT host,room,box,slice FROM room_leftovers WHERE host=? ORDER BY box='',room,box", host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Leftover
	for rows.Next() {
		var l Leftover
		if err := rows.Scan(&l.Host, &l.Room, &l.Box, &l.Slice); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LeftoverCounts tells each host's debts for host.list, so an owed cleanup stays visible.
func (s *Store) LeftoverCounts(ctx context.Context) (map[contract.HostName]int, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT host,COUNT(*) FROM room_leftovers GROUP BY host")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[contract.HostName]int)
	for rows.Next() {
		var host contract.HostName
		var count int
		if err := rows.Scan(&host, &count); err != nil {
			return nil, err
		}
		out[host] = count
	}
	return out, rows.Err()
}

// ForgetLeftover closes one debt after the host removed it.
func (s *Store) ForgetLeftover(ctx context.Context, l Leftover) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM room_leftovers WHERE host=? AND room=? AND box=?", l.Host, l.Room, l.Box)
		return err
	})
}
