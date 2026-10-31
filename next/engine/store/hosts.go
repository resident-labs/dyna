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

// RecordHostHeartbeat records reachability and a disk sample when available.
func (s *Store) RecordHostHeartbeat(ctx context.Context, name contract.HostName, reachable bool, freeDisk *int64, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if reachable && freeDisk != nil && *freeDisk < 20*box.GiB {
			var previous int64
			err := tx.QueryRowContext(ctx, "SELECT disk_bytes FROM hosts WHERE name=? AND state!='removed'", name).Scan(&previous)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil && previous >= 20*box.GiB {
				var id [16]byte
				if _, err := rand.Read(id[:]); err != nil {
					return err
				}
				event := contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.HostDiskLow{Host: name, FreeBytes: *freeDisk, FloorBytes: 20 * box.GiB}}
				if _, err := s.AppendEvent(tx, event); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE hosts SET
			state=CASE WHEN ? AND state='unreachable' THEN 'ready' WHEN NOT ? AND state='ready' THEN 'unreachable' ELSE state END,
			disk_bytes=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE disk_bytes END,
			checked_at=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE checked_at END
			WHERE name=? AND state!='removed'`, reachable, reachable, reachable, freeDisk, freeDisk, reachable, freeDisk, at.UnixMilli(), name)
		return err
	})
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

func (s *Store) Hosts(ctx context.Context) ([]box.Host, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT h.name, h.ssh, h.state, h.cpus, h.mem_bytes, h.disk_bytes, h.checked_at, h.maintained_at, h.engine_host, hr.room FROM hosts h LEFT JOIN host_rooms hr ON hr.host=h.name ORDER BY h.name,hr.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []box.Host
	for rows.Next() {
		var h box.Host
		var checked, maintained int64
		var room sql.NullString
		if err := rows.Scan(&h.Name, &h.SSH, &h.State, &h.CPUs, &h.MemBytes, &h.DiskBytes, &checked, &maintained, &h.EngineHost, &room); err != nil {
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

// ResetEngineHosts discards comparisons made by a previous engine process.
func (s *Store) ResetEngineHosts(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET engine_host=NULL")
		return err
	})
}

func (s *Store) SetEngineHost(ctx context.Context, name contract.HostName, value bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET engine_host=? WHERE name=?", value, name)
		return err
	})
}
