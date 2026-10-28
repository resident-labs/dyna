// Package doctoralert tells the root of each room with active agents when a
// machine is overloaded. It uses no model: the room's share of the load comes
// from the stored sessions, and the advice is a fixed rule. A room hears at
// most once per Interval while the overload lasts, then once when it ends.
package doctoralert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/database"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

// Interval is the shortest time between two alerts to one room.
const Interval = 30 * time.Minute

// Hold is how long a room stays quiet after a recovery note. An overload that
// returns inside Hold is a flap around the threshold, so it is not alerted.
const Hold = 30 * time.Minute

// RoomUse is the agent sessions one room has open on the machine.
type RoomUse struct {
	RoomID  string
	Room    string
	Running int
	Idle    int
	// Finished counts the idle sessions that hold no open work. Only
	// these have nothing left to do and may be closed.
	Finished int
}

// Sessions is the room's open session count.
func (u RoomUse) Sessions() int { return u.Running + u.Idle }

// Holds returns the sessions that own anything open (a goal, item, PR, question,
// child, shell job or workflow), from the counts by kind that
// subagent.Manager.ResponsibilityCounts reads. Closing such a session would
// disrupt that work.
func Holds(counts map[string]map[string]int) map[string]bool {
	out := map[string]bool{}
	for id, byKind := range counts {
		for _, n := range byKind {
			if n > 0 {
				out[id] = true
			}
		}
	}
	return out
}

// Count groups open sessions by room. A closed or dormant session holds no
// agent process, and a session with no room belongs to nobody to tell. An idle
// session is finished only when it is not in holds.
func Count(infos []subagent.Info, holds map[string]bool) []RoomUse {
	byRoom := map[string]*RoomUse{}
	for _, in := range infos {
		if in.RoomID == "" {
			continue
		}
		var running bool
		switch in.Status {
		case subagent.StatusRunning, subagent.StatusQueued:
			running = true
		case subagent.StatusIdle:
		default:
			continue
		}
		u := byRoom[in.RoomID]
		if u == nil {
			u = &RoomUse{RoomID: in.RoomID, Room: in.Room}
			byRoom[in.RoomID] = u
		}
		if running {
			u.Running++
		} else {
			u.Idle++
			if !holds[in.ID] {
				u.Finished++
			}
		}
	}
	out := make([]RoomUse, 0, len(byRoom))
	for _, u := range byRoom {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Sessions() > out[j].Sessions() || out[i].Sessions() == out[j].Sessions() && out[i].RoomID < out[j].RoomID
	})
	return out
}

// Note is what one room's root is told.
type Note struct {
	Room      RoomUse
	Machine   string
	Total     int // agent sessions on the machine, all rooms
	Report    doctor.Report
	Recovered bool
	// Outside lists the biggest programs on the machine that Dyna does not run,
	// each with the room it belongs to when that can be told.
	Outside []Outside
}

// Outside is a program outside Dyna and the room it belongs to, if known.
type Outside struct {
	Process doctor.Process
	Room    string
}

// Text is the line a note shows for it.
func (o Outside) Text() string { return o.Process.Text(o.Room) }

// Locate names the room of each outside program. The canonical room slice in
// its cgroup names the room directly. Otherwise a working folder inside a
// session's worktree names that session's room. An unrelated unit name proves
// no ownership.
func Locate(procs []doctor.Process, infos []subagent.Info) []Outside {
	sessionRoom := map[string]agentslice.RoomID{}
	roomNames := map[agentslice.RoomID]string{}
	for _, in := range infos {
		if in.Room == "" {
			continue
		}
		roomID := agentslice.RoomID(in.RoomID)
		sessionRoom[in.ID] = roomID
		roomNames[roomID] = in.Room
	}
	out := make([]Outside, len(procs))
	for i, p := range procs {
		roomID := sessionRoom[doctor.SessionIn(p.Cwd)]
		if p.RoomID != "" {
			roomID = p.RoomID
		}
		out[i] = Outside{Process: p, Room: roomNames[roomID]}
	}
	return out
}

// Advice is the one action the note asks of the room's root.
func (n Note) Advice() string {
	if n.Recovered {
		return fmt.Sprintf("%s is healthy again. No action is needed.", n.Machine)
	}
	share := fmt.Sprintf("this room runs %d of %d agent sessions on %s", n.Room.Sessions(), n.Total, n.Machine)
	if n.Room.Finished > 0 {
		return fmt.Sprintf("Close the %d idle sessions that have finished and hold no open work; %s.", n.Room.Finished, share)
	}
	if n.Room.Running > 0 {
		return fmt.Sprintf("Pause or stop %d of the %d running agents; %s.", (n.Room.Running+1)/2, n.Room.Running, share)
	}
	return fmt.Sprintf("No session is finished: the %d idle sessions still hold open work (a goal, item, PR, question, child, shell job or workflow). Finish or hand over that work, then close them; %s.", n.Room.Idle, share)
}

// Notifier decides which rooms to tell and remembers what it told them.
type Notifier struct {
	DataDir string
	Now     func() time.Time
	// Use lists the rooms' open sessions on the machine.
	Use func() ([]RoomUse, error)
	// Outside, when set, lists the biggest outside programs. It is read once
	// per pass, and only when a note is sent.
	Outside func() []Outside
	// Send posts one note to a room's root.
	Send func(Note) error
}

const schema = `CREATE TABLE IF NOT EXISTS doctor_room_alerts (
 machine TEXT NOT NULL, room_id TEXT NOT NULL, last_at INTEGER NOT NULL,
 recovered_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(machine, room_id)
)`

// Migrate brings the alert tables in dataDir's dyna.db to this version. Only
// the engine calls it, at start.
func Migrate(dataDir string) error {
	if _, err := (Notifier{DataDir: dataDir}).open(); err != nil {
		return err
	}
	_, err := (CapNotifier{DataDir: dataDir}).open()
	return err
}

func (n Notifier) open() (*sql.DB, error) {
	db, err := database.Open(n.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "doctor_room_alerts.version", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		if old == 1 {
			_, err := conn.ExecContext(ctx, `ALTER TABLE doctor_room_alerts ADD COLUMN recovered_at INTEGER NOT NULL DEFAULT 0`)
			return err
		}
		return nil
	})
}

func (n Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Alert tells every room with open sessions that its last note is older than
// Interval, or that it has none. A room that fails to send is tried again at
// the next pass, because only a sent note is recorded.
func (n Notifier) Alert(rep doctor.Report) error {
	rooms, err := n.Use()
	if err != nil {
		return err
	}
	total := 0
	for _, r := range rooms {
		total += r.Sessions()
	}
	db, err := n.open()
	if err != nil {
		return err
	}
	now := n.now()
	var errs []error
	var outside []Outside
	outsideRead := false
	for _, r := range rooms {
		var last, recovered int64
		err := db.QueryRow(`SELECT last_at, recovered_at FROM doctor_room_alerts WHERE machine=? AND room_id=?`, rep.Machine, r.RoomID).Scan(&last, &recovered)
		if err == nil && recovered > 0 && now.Sub(time.Unix(recovered, 0)) < Hold {
			continue
		}
		if err == nil && recovered == 0 && now.Sub(time.Unix(last, 0)) < Interval {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			errs = append(errs, err)
			continue
		}
		if !outsideRead && n.Outside != nil {
			outside, outsideRead = n.Outside(), true
		}
		if err := n.Send(Note{Room: r, Machine: rep.Machine, Total: total, Report: rep, Outside: outside}); err != nil {
			errs = append(errs, fmt.Errorf("room %s: %w", r.Room, err))
			continue
		}
		ctx := context.Background()
		errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO doctor_room_alerts VALUES (?,?,?,0)`, rep.Machine, r.RoomID, now.Unix())
			return err
		}))
	}
	return errors.Join(errs...)
}

// Recover sends one recovered note to every room that was told about machine
// and has not yet heard of a recovery. It marks them recovered, so an overload
// that returns within Hold stays quiet; after Hold the next overload alerts.
func (n Notifier) Recover(machine string) error {
	db, err := n.open()
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT room_id FROM doctor_room_alerts WHERE machine=? AND recovered_at=0`, machine)
	if err != nil {
		return err
	}
	var told []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		told = append(told, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(told) == 0 {
		return nil
	}
	rooms, err := n.Use()
	if err != nil {
		return err
	}
	byID := map[string]RoomUse{}
	for _, r := range rooms {
		byID[r.RoomID] = r
	}
	var errs []error
	for _, id := range told {
		// A room with no open session still hears that the overload ended.
		r, ok := byID[id]
		if !ok {
			r = RoomUse{RoomID: id}
		}
		if err := n.Send(Note{Room: r, Machine: machine, Recovered: true}); err != nil {
			errs = append(errs, err)
			continue
		}
		ctx := context.Background()
		errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, `UPDATE doctor_room_alerts SET recovered_at=? WHERE machine=? AND room_id=?`, n.now().Unix(), machine, id)
			return err
		}))
	}
	return errors.Join(errs...)
}
