// The caps of a room's slice: a room hears that its work nears a cap once
// per cap and crossing, and only again after it fell clearly back under the
// cap. The state is one SQLite row per room and cap (r1).

package doctoralert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// CapAlertShare is the use of a cap, as a percent of the limit, that alerts.
const CapAlertShare = 80

// CapRearmShare is the use below which a cap's alert re-arms, so a later
// crossing alerts again. The gap to CapAlertShare keeps a room hovering at
// the threshold from alerting every pass.
const CapRearmShare = 70

// CapTopTrees is how many process trees a cap note lists each way.
const CapTopTrees = 5

// CapNote is one room's use of one cap, as its root and the owner read it.
type CapNote struct {
	RoomID string
	Cap    doctor.Cap
	// Current and Limit are the room's use and the cap itself: bytes for the
	// memory caps, processes for pids.max.
	Current, Limit uint64
	// Trees lists the room's biggest process trees, by memory and by count.
	Trees doctor.CapTrees
	At    time.Time
}

// CapNotifier tells the root of a room and the owner when the room's work
// nears one cap of its resource slice: once per cap, until the room falls
// back under CapRearmShare and crosses again.
type CapNotifier struct {
	DataDir string
	Now     func() time.Time
	// Read returns each room's use of its slice caps.
	Read func() ([]doctor.RoomUse, error)
	// Trees returns one room's biggest process trees. It runs only when an
	// alert is sent.
	Trees func(roomID string) doctor.CapTrees
	// Send posts one note.
	Send func(CapNote) error
}

const capSchema = `CREATE TABLE IF NOT EXISTS room_cap_alerts (
 room_id TEXT NOT NULL, cap TEXT NOT NULL, at INTEGER NOT NULL,
 PRIMARY KEY(room_id, cap)
)`

func (n CapNotifier) open() (*sql.DB, error) {
	db, err := database.Open(n.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "room_cap_alerts.version", 1, func(conn *sql.Conn, old int) error {
		_, err := conn.ExecContext(context.Background(), capSchema)
		return err
	})
}

func (n CapNotifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Alert examines every room's caps and sends one note per cap that crossed
// CapAlertShare with no alert active, then records it. A cap that fell back
// under CapRearmShare re-arms. A room that fails to send is tried again at
// the next pass, because only a sent note is recorded.
func (n CapNotifier) Alert() error {
	uses, err := n.Read()
	if err != nil {
		return err
	}
	db, err := n.open()
	if err != nil {
		return err
	}
	now := n.now()
	ctx := context.Background()
	var errs []error
	for _, use := range uses {
		alerted := map[doctor.Cap]bool{}
		rows, err := db.Query(`SELECT cap FROM room_cap_alerts WHERE room_id=?`, use.RoomID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var scanErr error
		for rows.Next() {
			var cap string
			if err := rows.Scan(&cap); err != nil {
				scanErr = err
				break
			}
			alerted[doctor.Cap(cap)] = true
		}
		if err := rows.Close(); err != nil {
			errs = append(errs, err)
			continue
		}
		// A row that cannot be read could hide an active alert, so the room
		// waits for the next pass rather than hear a second one.
		if scanErr != nil {
			errs = append(errs, scanErr)
			continue
		}
		var trees doctor.CapTrees
		treesRead := false
		for _, c := range use.Caps {
			switch {
			case c.Current*100 >= c.Limit*CapAlertShare && !alerted[c.Cap]:
				if !treesRead && n.Trees != nil {
					trees, treesRead = n.Trees(use.RoomID), true
				}
				if err := n.Send(CapNote{RoomID: use.RoomID, Cap: c.Cap, Current: c.Current, Limit: c.Limit, Trees: trees, At: now}); err != nil {
					errs = append(errs, fmt.Errorf("room %s cap %s: %w", use.RoomID, c.Cap, err))
					continue
				}
				alerted[c.Cap] = true
				errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO room_cap_alerts VALUES (?,?,?)`, use.RoomID, string(c.Cap), now.Unix())
					return err
				}))
			case c.Current*100 < c.Limit*CapRearmShare && alerted[c.Cap]:
				alerted[c.Cap] = false
				errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(ctx, `DELETE FROM room_cap_alerts WHERE room_id=? AND cap=?`, use.RoomID, string(c.Cap))
					return err
				}))
			}
		}
	}
	return errors.Join(errs...)
}
