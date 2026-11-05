package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (s *Store) AddHost(ctx context.Context, h box.Host) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO hosts (name, ssh, state, cpus, mem_bytes, disk_bytes, checked_at, engine_host, agents, ci, create_problem, exec_only) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", h.Name, h.SSH, h.State, h.CPUs, h.MemBytes, h.DiskBytes, h.CheckedAt.UnixMilli(), h.EngineHost, !h.NoAgents, h.CI, h.Problem, h.ExecOnly)
		if err != nil {
			return err
		}
		for _, room := range h.Rooms {
			if err := refuseNewWork(ctx, tx, room); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO host_rooms(host,room) VALUES(?,?)", h.Name, room); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetHostRooms replaces a host's room limit in one transaction. SetAccountRooms is its twin for accounts.
func (s *Store) SetHostRooms(ctx context.Context, host contract.HostName, rooms []contract.RoomID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var name contract.HostName
		if err := tx.QueryRowContext(ctx, "SELECT name FROM hosts WHERE name=?", host).Scan(&name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM host_rooms WHERE host=?", host); err != nil {
			return err
		}
		for _, room := range rooms {
			if err := refuseNewWork(ctx, tx, room); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO host_rooms(host,room) VALUES(?,?)", host, room); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetHostSwitches turns a host's agents switch, its CI switch or both; nil leaves one as it is. Boxes and runners already on
// the host stay.
func (s *Store) SetHostSwitches(ctx context.Context, host contract.HostName, agents, ci *bool, clearCreateProblem bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE hosts SET agents=coalesce(?2,agents), ci=coalesce(?3,ci), create_failures=CASE WHEN ?4 THEN 0 ELSE create_failures END, create_problem=CASE WHEN ?4 THEN '' ELSE create_problem END, create_generation=create_generation+CASE WHEN ?4 THEN 1 ELSE 0 END WHERE name=?1", host, agents, ci, clearCreateProblem)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			err = fmt.Errorf("host %s: %w", host, ErrNotFound)
		}
		return err
	})
}

// HostHeldError refuses to remove a host that still holds work, and says what.
type HostHeldError struct {
	Host contract.HostName
	Held []string
}

func (e *HostHeldError) Error() string {
	return fmt.Sprintf("host %s still holds %s", e.Host, strings.Join(e.Held, ", "))
}

// hostHeld reads what keeps a host, so nothing that needs it goes with it. A deleted box holds nothing, though it keeps its host.
// Without boxes, only what is not a box counts, for a removal that discards the boxes.
func hostHeld(ctx context.Context, tx *sql.Tx, host contract.HostName, boxes bool) error {
	held, err := listIDs[string](ctx, tx, `SELECT 'session '||s.name||' ('||b.state||')' FROM boxes b JOIN sessions s ON s.id=b.session WHERE ?2 AND b.host=?1 AND b.state!='deleted'
		UNION ALL SELECT 'service '||json_extract(spec,'$.name') FROM services WHERE ?2 AND host=?1 AND NOT (state='failed' AND reason='host_removed')
		UNION ALL SELECT 'a subscription run' FROM subscription_runs WHERE host=?1
		UNION ALL SELECT 'the --only rule of room '||r.id FROM rooms r, json_each(r.only_hosts) j WHERE j.value=?1`, host, boxes)
	if err != nil {
		return err
	}
	if len(held) > 0 {
		return &HostHeldError{Host: host, Held: held}
	}
	return nil
}

// DrainHost stops admission to a host that holds nothing, in one write with that check, so no box can land on it afterwards.
func (s *Store) DrainHost(ctx context.Context, host contract.HostName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := hostHeld(ctx, tx, host, true); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE hosts SET state='draining' WHERE name=? AND state!='provisioning'", host)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			err = fmt.Errorf("host %s: %w", host, ErrNotFound)
		}
		return err
	})
}

// RemoveHost deletes a host that holds nothing. Its deleted boxes let go of it, and what it still owed a deleted room goes with it.
func (s *Store) RemoveHost(ctx context.Context, host contract.HostName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := hostHeld(ctx, tx, host, true); err != nil {
			return err
		}
		for _, step := range []string{
			"UPDATE boxes SET host=NULL WHERE host=?1",
			"DELETE FROM host_rooms WHERE host=?1",
			"DELETE FROM host_labels WHERE host=?1",
			"DELETE FROM room_leftovers WHERE host=?1",
			"DELETE FROM service_leftovers WHERE host=?1",
			"UPDATE services SET host=NULL WHERE host=?1",
			"DELETE FROM disk_warnings WHERE host=?1",
			"DELETE FROM hosts WHERE name=?1",
		} {
			if _, err := tx.ExecContext(ctx, step, host); err != nil {
				return err
			}
		}
		return nil
	})
}

// diskAlertGap is the least time between alerts for one host. The btrfs metadata rule moves free space straight to zero and back,
// so a host near that line leaves the warn band on every other sample, which free-space hysteresis alone would alert on each time.
const diskAlertGap = time.Hour

// RecordHostHeartbeat records reachability and a disk sample when available. diskTotal sets the floor that sample is held to.
// A report that is not a sample at the floor, including a miss or a failed probe, restarts the wait before eviction.
func (s *Store) RecordHostHeartbeat(ctx context.Context, name contract.HostName, reachable bool, freeDisk *int64, diskTotal int64, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		floor := box.DiskFloor(diskTotal)
		var low int64
		if reachable && freeDisk != nil {
			if err := s.warnDisk(ctx, tx, name, *freeDisk, floor, at); err != nil {
				return err
			}
			if *freeDisk <= floor {
				low = at.UnixMilli()
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE hosts SET
			state=CASE WHEN ? AND state='unreachable' THEN 'ready' WHEN NOT ? AND state='ready' THEN 'unreachable' ELSE state END,
			disk_bytes=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE disk_bytes END,
			disk_floor=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE disk_floor END,
			disk_total=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE disk_total END,
			disk_low_since=CASE WHEN ?=0 OR disk_low_since=0 THEN ? ELSE disk_low_since END,
			checked_at=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE checked_at END
			WHERE name=? AND state!='removed'`, reachable, reachable, reachable, freeDisk, freeDisk, reachable, freeDisk, floor, reachable, freeDisk, diskTotal, low, low, reachable, freeDisk, at.UnixMilli(), name)
		return err
	})
}

// warnDisk alerts room roots once per low spell. A spell ends a tenth above the floor and diskAlertGap after its alert, so a host that hovers stays quiet.
func (s *Store) warnDisk(ctx context.Context, tx *sql.Tx, name contract.HostName, free, floor int64, at time.Time) error {
	var execOnly bool
	if err := tx.QueryRowContext(ctx, "SELECT exec_only FROM hosts WHERE name=?", name).Scan(&execOnly); err != nil && err != sql.ErrNoRows {
		return err
	}
	// An exec-only host has no disk figures to warn about.
	if execOnly {
		return nil
	}
	if free >= floor+floor/10 {
		_, err := tx.ExecContext(ctx, "DELETE FROM disk_warnings WHERE host=? AND at<=?", name, at.Add(-diskAlertGap).UnixMilli())
		return err
	}
	if !box.DiskLow(free, floor) {
		return nil
	}
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO disk_warnings(host,at) VALUES(?,?)", name, at.UnixMilli())
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return err
	}
	return s.alertRoots(ctx, tx, contract.HostDiskLow{Host: name, FreeBytes: free, FloorBytes: floor}, at)
}

// alertRoots posts the event in every room and wakes its root, which can close finished work.
func (s *Store) alertRoots(ctx context.Context, tx *sql.Tx, body contract.HostDiskLow, at time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,room FROM sessions WHERE parent IS NULL AND state='open' AND closed_at IS NULL")
	if err != nil {
		return err
	}
	var roots []Session
	for rows.Next() {
		var root Session
		if err := rows.Scan(&root.ID, &root.Room); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, root)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, root := range roots {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Room: root.Room, Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body})
		if err != nil {
			return err
		}
		if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: root.ID}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ClaimHostMaintenance(ctx context.Context, name contract.HostName, now time.Time) (bool, error) {
	var claimed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE hosts SET maintained_at=? WHERE name=? AND state='ready' AND maintained_at<=?", now.Add(-23*time.Hour).UnixMilli(), name, now.Add(-24*time.Hour).UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		claimed = n == 1
		return err
	})
	return claimed, err
}

func (s *Store) FinishHostMaintenance(ctx context.Context, name contract.HostName, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET maintained_at=? WHERE name=?", now.UnixMilli(), name)
		return err
	})
}

// SetRoomShared replaces the measured size of every room's shared folder.
func (s *Store) SetRoomShared(ctx context.Context, sizes []contract.RoomShared, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM room_shared"); err != nil {
			return err
		}
		for _, size := range sizes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO room_shared(room,bytes,measured_at) VALUES(?,?,?)", size.Room, size.Bytes, now.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) RoomShared(ctx context.Context) ([]contract.RoomShared, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT room,bytes FROM room_shared ORDER BY room")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.RoomShared{}
	for rows.Next() {
		var size contract.RoomShared
		if err := rows.Scan(&size.Room, &size.Bytes); err != nil {
			return nil, err
		}
		out = append(out, size)
	}
	return out, rows.Err()
}

// HostSessions lists each host's sessions, by room and then oldest first. A deleted box holds nothing, though it keeps
// its host, and a box not placed yet has none. A session closes only after its box is deleted, so every row is open.
func (s *Store) HostSessions(ctx context.Context) (map[contract.HostName][]contract.HostSession, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT b.host,s.room,s.id,s.name,b.state FROM boxes b JOIN sessions s ON s.id=b.session WHERE COALESCE(b.host,'')!='' AND b.state!=? ORDER BY b.host,s.room,s.created_at,s.id", contract.BoxDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[contract.HostName][]contract.HostSession)
	for rows.Next() {
		var host contract.HostName
		var session contract.HostSession
		if err := rows.Scan(&host, &session.Room, &session.ID, &session.Name, &session.BoxState); err != nil {
			return nil, err
		}
		out[host] = append(out[host], session)
	}
	return out, rows.Err()
}

// Hosts lists the hosts. A pool's node being created has no address yet, so it is not one.
func (s *Store) Hosts(ctx context.Context) ([]box.Host, error) {
	return s.hosts(ctx, "h.state!='provisioning'")
}

func (s *Store) hosts(ctx context.Context, where string) ([]box.Host, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT h.name, h.ssh, h.state, h.cpus, h.mem_bytes, h.disk_bytes, h.disk_floor, h.disk_total, h.checked_at, h.maintained_at, h.engine_host, h.pool, h.box, h.agents, h.ci, h.create_problem, h.exec_only, h.gpus, hr.room FROM hosts h LEFT JOIN host_rooms hr ON hr.host=h.name WHERE `+where+` ORDER BY h.name,hr.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []box.Host
	for rows.Next() {
		var h box.Host
		var checked, maintained int64
		var room sql.NullString
		var agents bool
		var gpus string
		if err := rows.Scan(&h.Name, &h.SSH, &h.State, &h.CPUs, &h.MemBytes, &h.DiskBytes, &h.DiskFloor, &h.DiskTotal, &checked, &maintained, &h.EngineHost, &h.Pool, &h.Box, &agents, &h.CI, &h.Problem, &h.ExecOnly, &gpus, &room); err != nil {
			return nil, err
		}
		if len(hosts) == 0 || hosts[len(hosts)-1].Name != h.Name {
			h.NoAgents = !agents
			if err := json.Unmarshal([]byte(gpus), &h.GPUs); err != nil {
				return nil, err
			}
			h.CheckedAt = time.UnixMilli(checked)
			if maintained != 0 {
				h.MaintainedAt = time.UnixMilli(maintained)
			}
			hosts = append(hosts, h)
		}
		if room.Valid {
			hosts[len(hosts)-1].Rooms = append(hosts[len(hosts)-1].Rooms, contract.RoomID(room.String))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	holds, owners, err := serviceHolds(ctx, s.Read())
	if err != nil {
		return nil, err
	}
	for i, hold := range holds {
		for j := range hosts {
			if hosts[j].Name == owners[i] {
				hosts[j].Services = append(hosts[j].Services, hold)
			}
		}
	}
	return hosts, nil
}

// ResetEngineHosts discards comparisons and low-disk spells from a previous engine process. Its samples may be long stale.
func (s *Store) ResetEngineHosts(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET engine_host=NULL,disk_low_since=0")
		return err
	})
}

func (s *Store) SetEngineHost(ctx context.Context, name contract.HostName, value bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET engine_host=? WHERE name=?", value, name)
		return err
	})
}

// ExecOnly reports whether a host was added exec only and has not been turned into a full host since. The control plane is not a stored host.
func (s *Store) ExecOnly(ctx context.Context, name contract.HostName) (bool, error) {
	var execOnly bool
	err := s.Read().QueryRowContext(ctx, "SELECT exec_only FROM hosts WHERE name=?", name).Scan(&execOnly)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return execOnly, err
}

// SetHostChecked records what the full host check found and ends exec-only: the host is about to take boxes or runners.
func (s *Store) SetHostChecked(ctx context.Context, h box.Host) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET exec_only=0, cpus=?, mem_bytes=?, disk_bytes=?, checked_at=?, engine_host=?, create_problem=? WHERE name=?", h.CPUs, h.MemBytes, h.DiskBytes, h.CheckedAt.UnixMilli(), h.EngineHost, h.Problem, h.Name)
		return err
	})
}

// SetHostGPUs records the cards a host's latest probe found.
func (s *Store) SetHostGPUs(ctx context.Context, host contract.HostName, gpus []contract.GPU) error {
	if gpus == nil {
		gpus = []contract.GPU{}
	}
	text, err := json.Marshal(gpus)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET gpus=? WHERE name=?", text, host)
		return err
	})
}
