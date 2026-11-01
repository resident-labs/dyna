package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (s *Store) AddHost(ctx context.Context, h box.Host) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO hosts (name, ssh, state, cpus, mem_bytes, disk_bytes, checked_at, engine_host) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", h.Name, h.SSH, h.State, h.CPUs, h.MemBytes, h.DiskBytes, h.CheckedAt.UnixMilli(), h.EngineHost)
		if err != nil {
			return err
		}
		for _, room := range h.Rooms {
			if _, err := tx.ExecContext(ctx, "INSERT INTO host_rooms(host,room) VALUES(?,?)", h.Name, room); err != nil {
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
			disk_low_since=CASE WHEN ?=0 OR disk_low_since=0 THEN ? ELSE disk_low_since END,
			checked_at=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE checked_at END
			WHERE name=? AND state!='removed'`, reachable, reachable, reachable, freeDisk, freeDisk, reachable, freeDisk, floor, low, low, reachable, freeDisk, at.UnixMilli(), name)
		return err
	})
}

// warnDisk alerts room roots once per low spell. A spell ends a tenth above the warn level and diskAlertGap after its alert, so a host that hovers stays quiet.
func (s *Store) warnDisk(ctx context.Context, tx *sql.Tx, name contract.HostName, free, floor int64, at time.Time) error {
	warn := box.DiskWarn(floor)
	if free >= warn+warn/10 {
		_, err := tx.ExecContext(ctx, "DELETE FROM disk_warnings WHERE host=? AND at<=?", name, at.Add(-diskAlertGap).UnixMilli())
		return err
	}
	if free >= warn {
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

// AlertDisk tells room roots that work could not run for lack of disk.
func (s *Store) AlertDisk(ctx context.Context, name contract.HostName, free, need int64, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		return s.alertRoots(ctx, tx, contract.HostDiskLow{Host: name, FreeBytes: free, FloorBytes: need}, at)
	})
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

func (s *Store) FinishHostMaintenance(ctx context.Context, name contract.HostName, sizes []contract.RoomShared, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM room_shared WHERE host=?", name); err != nil {
			return err
		}
		for _, size := range sizes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO room_shared(room,host,bytes,measured_at) VALUES(?,?,?,?)", size.Room, name, size.Bytes, now.UnixMilli()); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET maintained_at=? WHERE name=?", now.UnixMilli(), name)
		return err
	})
}

func (s *Store) RoomShared(ctx context.Context) (map[contract.HostName][]contract.RoomShared, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT host,room,bytes FROM room_shared ORDER BY host,room")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[contract.HostName][]contract.RoomShared)
	for rows.Next() {
		var host contract.HostName
		var size contract.RoomShared
		if err := rows.Scan(&host, &size.Room, &size.Bytes); err != nil {
			return nil, err
		}
		out[host] = append(out[host], size)
	}
	return out, rows.Err()
}

func (s *Store) Hosts(ctx context.Context) ([]box.Host, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT h.name, h.ssh, h.state, h.cpus, h.mem_bytes, h.disk_bytes, h.disk_floor, h.checked_at, h.maintained_at, h.engine_host, hr.room FROM hosts h LEFT JOIN host_rooms hr ON hr.host=h.name ORDER BY h.name,hr.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []box.Host
	for rows.Next() {
		var h box.Host
		var checked, maintained int64
		var room sql.NullString
		if err := rows.Scan(&h.Name, &h.SSH, &h.State, &h.CPUs, &h.MemBytes, &h.DiskBytes, &h.DiskFloor, &checked, &maintained, &h.EngineHost, &room); err != nil {
			return nil, err
		}
		if len(hosts) == 0 || hosts[len(hosts)-1].Name != h.Name {
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
	return hosts, rows.Err()
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
