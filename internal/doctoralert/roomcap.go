// The caps of a room's slice: a room hears that its work nears a cap once
// per cap and crossing, and only again after it fell clearly back under the
// cap. The state is one SQLite row per room and cap (r1).

package doctoralert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/bits"
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
 sent INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(room_id, cap)
)`

func (n CapNotifier) open() (*sql.DB, error) {
	db, err := database.Open(n.DataDir)
	if err != nil {
		return nil, err
	}
	return db, database.Migrate(db, "room_cap_alerts.version", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, capSchema); err != nil {
			return err
		}
		if old == 1 {
			// Rows written by version 1 are alerts that were already sent.
			_, err := conn.ExecContext(ctx, `ALTER TABLE room_cap_alerts ADD COLUMN sent INTEGER NOT NULL DEFAULT 1`)
			return err
		}
		return nil
	})
}

func (n CapNotifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// capAlert is the durable state of one crossing. A pending row holds the
// alert's original time until Send succeeds and the row becomes sent.
type capAlert struct {
	at   int64
	sent bool
}

// Alert examines every room's caps and sends one note per cap that crossed
// CapAlertShare with no alert active. It stores a pending row before Send, so
// a retry reuses the crossing's time and therefore the same typed event ID.
// A cap that fell back under CapRearmShare re-arms.
func (n CapNotifier) Alert() error {
	uses, err := n.Read()
	if err != nil {
		return err
	}
	db, err := n.open()
	if err != nil {
		return err
	}
	// The state row stores whole seconds, so the posted time does too.
	now := n.now().Truncate(time.Second)
	ctx := context.Background()
	var errs []error
	for _, use := range uses {
		alerts := map[doctor.Cap]capAlert{}
		rows, err := db.Query(`SELECT cap, at, sent FROM room_cap_alerts WHERE room_id=?`, use.RoomID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var scanErr error
		for rows.Next() {
			var (
				cap  string
				at   int64
				sent int
			)
			if err := rows.Scan(&cap, &at, &sent); err != nil {
				scanErr = err
				break
			}
			alerts[doctor.Cap(cap)] = capAlert{at: at, sent: sent != 0}
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
			alert, recorded := alerts[c.Cap]
			switch {
			case capAtLeast(c.Current, c.Limit, CapAlertShare) && (!recorded || !alert.sent):
				at := now
				if recorded {
					// A pending alert keeps the time of its first attempt.
					at = time.Unix(alert.at, 0)
				} else {
					err := state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
						_, err := conn.ExecContext(ctx, `INSERT INTO room_cap_alerts (room_id, cap, at, sent) VALUES (?,?,?,0)`, use.RoomID, string(c.Cap), at.Unix())
						return err
					})
					if err != nil {
						errs = append(errs, err)
						continue
					}
				}
				if !treesRead && n.Trees != nil {
					trees, treesRead = n.Trees(use.RoomID), true
				}
				if err := n.Send(CapNote{RoomID: use.RoomID, Cap: c.Cap, Current: c.Current, Limit: c.Limit, Trees: trees, At: at}); err != nil {
					errs = append(errs, fmt.Errorf("room %s cap %s: %w", use.RoomID, c.Cap, err))
					continue
				}
				alerts[c.Cap] = capAlert{at: at.Unix(), sent: true}
				errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(ctx, `UPDATE room_cap_alerts SET sent=1 WHERE room_id=? AND cap=?`, use.RoomID, string(c.Cap))
					return err
				}))
			case !capAtLeast(c.Current, c.Limit, CapRearmShare) && recorded:
				delete(alerts, c.Cap)
				errs = append(errs, state.For(db).Write(ctx, hub.Event{Topic: "doctor"}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(ctx, `DELETE FROM room_cap_alerts WHERE room_id=? AND cap=?`, use.RoomID, string(c.Cap))
					return err
				}))
			}
		}
	}
	return errors.Join(errs...)
}

// capAtLeast reports current*100 >= limit*share. The full 128-bit products
// matter: either side can overflow uint64 for a large memory cap.
func capAtLeast(current, limit, share uint64) bool {
	currentHi, currentLo := bits.Mul64(current, 100)
	limitHi, limitLo := bits.Mul64(limit, share)
	return currentHi > limitHi || (currentHi == limitHi && currentLo >= limitLo)
}
