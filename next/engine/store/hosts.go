package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (s *Store) AddHost(ctx context.Context, h box.Host) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO hosts (name, ssh, state, cpus, mem_bytes, disk_bytes, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?)", h.Name, h.SSH, h.State, h.CPUs, h.MemBytes, h.DiskBytes, h.CheckedAt.UnixMilli())
		return err
	})
}

// RecordHostHeartbeat records reachability and a disk sample when available.
func (s *Store) RecordHostHeartbeat(ctx context.Context, name contract.HostName, reachable bool, freeDisk *int64, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE hosts SET
			state=CASE WHEN ? AND state='unreachable' THEN 'ready' WHEN NOT ? AND state='ready' THEN 'unreachable' ELSE state END,
			disk_bytes=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE disk_bytes END,
			checked_at=CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE checked_at END
			WHERE name=? AND state!='removed'`, reachable, reachable, reachable, freeDisk, freeDisk, reachable, freeDisk, at.UnixMilli(), name)
		return err
	})
}

func (s *Store) Hosts(ctx context.Context) ([]box.Host, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT name, ssh, state, cpus, mem_bytes, disk_bytes, checked_at FROM hosts ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []box.Host
	for rows.Next() {
		var h box.Host
		var checked int64
		if err := rows.Scan(&h.Name, &h.SSH, &h.State, &h.CPUs, &h.MemBytes, &h.DiskBytes, &checked); err != nil {
			return nil, err
		}
		h.CheckedAt = time.UnixMilli(checked)
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}
