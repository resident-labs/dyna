package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// ErrPoolExists refuses a second pool of one name.
var ErrPoolExists = errors.New("pool already exists")

// PoolInUseError refuses to remove a pool that still has nodes, or that a room's rule names.
type PoolInUseError struct {
	Name         string
	Nodes, Rooms []string
}

func (e *PoolInUseError) Error() string {
	var held []string
	if len(e.Nodes) > 0 {
		held = append(held, "nodes "+strings.Join(e.Nodes, ", "))
	}
	if len(e.Rooms) > 0 {
		held = append(held, "the --only rule of "+strings.Join(e.Rooms, ", "))
	}
	return fmt.Sprintf("pool %s still has %s", e.Name, strings.Join(held, " and "))
}

// Pool is one node pool. Its nodes are rows in hosts with pool set, and everything else about a machine lives there.
type Pool struct {
	Name       string
	Max        int
	PerSession bool
	Idle       time.Duration
	// A failed create or destroy backs the pool off until RetryAt, and only the first failure of a spell posts a notice.
	Failures int
	RetryAt  time.Time
	Problem  string
	// Told holds the unknown machines the owner has heard about, so each is reported once.
	Told []string
}

// NewOrphans lists the unknown machines this pool has not reported yet.
func (p Pool) NewOrphans(unknown []string) []string {
	return slices.DeleteFunc(slices.Clone(unknown), func(name string) bool { return slices.Contains(p.Told, name) })
}

func (s *Store) AddPool(ctx context.Context, p Pool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO pools(name,max,per_session,idle_ms) VALUES(?,?,?,?)", p.Name, p.Max, p.PerSession, p.Idle.Milliseconds())
		return noRows(result, err, fmt.Errorf("%w: %s", ErrPoolExists, p.Name))
	})
}

// RemovePool deletes an empty pool that no room names, so no rule ever points at nothing.
func (s *Store) RemovePool(ctx context.Context, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		nodes, err := listIDs[string](ctx, tx, "SELECT name FROM hosts WHERE pool=? ORDER BY name", name)
		if err != nil {
			return err
		}
		rooms, err := listIDs[string](ctx, tx, "SELECT r.id FROM rooms r, json_each(r.only_hosts) j WHERE j.value=? ORDER BY r.id", name)
		if err != nil {
			return err
		}
		if len(nodes)+len(rooms) > 0 {
			return &PoolInUseError{Name: name, Nodes: nodes, Rooms: rooms}
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM pools WHERE name=?", name)
		return noRows(result, err, fmt.Errorf("pool %s: %w", name, ErrNotFound))
	})
}

// SetPool changes what it is given of a pool's max, per-session switch and idle time. ErrNotFound means no pool has the name.
func (s *Store) SetPool(ctx context.Context, name string, max *int, perSession *bool, idle *time.Duration) error {
	var idleMS *int64
	if idle != nil {
		ms := idle.Milliseconds()
		idleMS = &ms
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE pools SET max=COALESCE(?,max), per_session=COALESCE(?,per_session), idle_ms=COALESCE(?,idle_ms) WHERE name=?", max, perSession, idleMS, name)
		return noRows(result, err, ErrNotFound)
	})
}

func (s *Store) Pools(ctx context.Context) ([]Pool, error) {
	return queryAll(ctx, s.Read(), func(rows scanner) (Pool, error) {
		var p Pool
		var idle, retry int64
		var told string
		if err := rows.Scan(&p.Name, &p.Max, &p.PerSession, &idle, &p.Failures, &retry, &p.Problem, &told); err != nil {
			return p, err
		}
		p.Idle = time.Duration(idle) * time.Millisecond
		if retry != 0 {
			p.RetryAt = time.UnixMilli(retry)
		}
		if err := json.Unmarshal([]byte(told), &p.Told); err != nil {
			return p, err
		}
		return p, nil
	}, "SELECT name,max,per_session,idle_ms,failures,retry_at,problem,told FROM pools ORDER BY name")
}

// PoolNodes lists every pool's nodes, counting those being created.
func (s *Store) PoolNodes(ctx context.Context) ([]box.Host, error) {
	return s.hosts(ctx, "h.pool!=''")
}

// StartPoolNode records a node before its script runs, so a restart cannot lose count of it. It reports false when the pool is
// full or this box already has its node, and that check and the row are one write. A node has ci on: it serves the CI runners
// of the rooms that name its pool, as it serves their boxes.
func (s *Store) StartPoolNode(ctx context.Context, p Pool, node box.Host) (bool, error) {
	started := false
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var taken bool
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM hosts WHERE pool=?1)>=?2 OR (?3!='' AND EXISTS (SELECT 1 FROM hosts WHERE box=?3))", p.Name, p.Max, node.Box).Scan(&taken); err != nil || taken {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,pool,box,ci) VALUES(?,'',?,0,0,0,0,?,?,1)", node.Name, box.HostProvisioning, p.Name, node.Box); err != nil {
			return err
		}
		started = true
		if node.Box != "" {
			_, err := tx.ExecContext(ctx, "INSERT INTO host_rooms(host,room) SELECT ?,id FROM rooms WHERE id=? AND state='open'", node.Name, node.Rooms[0])
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO host_rooms(host,room) SELECT ?,r.id FROM rooms r, json_each(r.only_hosts) j WHERE j.value=? AND r.state='open'", node.Name, p.Name)
		return err
	})
	return started, err
}

// FinishPoolNode makes a node a ready host and ends its pool's failures. Its first daily pass comes in a day: a new node builds
// what its box needs and no more.
func (s *Store) FinishPoolNode(ctx context.Context, h box.Host) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE hosts SET ssh=?,state='ready',cpus=?,mem_bytes=?,disk_bytes=?,checked_at=?,maintained_at=?,engine_host=?,create_problem=? WHERE name=? AND state='provisioning'", h.SSH, h.CPUs, h.MemBytes, h.DiskBytes, h.CheckedAt.UnixMilli(), h.CheckedAt.UnixMilli(), h.EngineHost, h.Problem, h.Name)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			err = fmt.Errorf("node %s: %w", h.Name, ErrNotFound)
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE pools SET failures=0,retry_at=0,problem='' WHERE name=?", h.Pool)
		return err
	})
}

// PoolFailed backs a pool off, and posts the notice only for the first failure of a spell.
func (s *Store) PoolFailed(ctx context.Context, name, problem string, delay func(failures int) time.Duration, notice contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var failures int
		if err := tx.QueryRowContext(ctx, "SELECT failures FROM pools WHERE name=?", name).Scan(&failures); err != nil {
			return err
		}
		failures++
		if _, err := tx.ExecContext(ctx, "UPDATE pools SET failures=?,retry_at=?,problem=? WHERE name=?", failures, notice.Time.Add(delay(failures)).UnixMilli(), problem, name); err != nil {
			return err
		}
		if failures > 1 {
			return nil
		}
		_, err := s.AppendEvent(tx, notice)
		return err
	})
}

// PoolRecovered ends a spell that a later success showed to be over.
func (s *Store) PoolRecovered(ctx context.Context, name string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE pools SET failures=0,retry_at=0,problem='' WHERE name=?", name)
		return err
	})
}

// PoolOrphans remembers the unknown machines the script still lists, and posts the notice about any new one.
func (s *Store) PoolOrphans(ctx context.Context, name string, unknown []string, notice *contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		told, _ := json.Marshal(append([]string{}, unknown...))
		if _, err := tx.ExecContext(ctx, "UPDATE pools SET told=? WHERE name=?", string(told), name); err != nil {
			return err
		}
		if notice == nil {
			return nil
		}
		_, err := s.AppendEvent(tx, *notice)
		return err
	})
}

// PoolRoom picks the room that hears about a pool when no box speaks for it: the first that names it, or else the first open one.
func (s *Store) PoolRoom(ctx context.Context, name string) (contract.RoomID, error) {
	rooms, err := listIDs[contract.RoomID](ctx, s.Read(), "SELECT r.id FROM rooms r WHERE r.state='open' ORDER BY EXISTS (SELECT 1 FROM json_each(r.only_hosts) j WHERE j.value=?) DESC,r.id LIMIT 1", name)
	if err != nil || len(rooms) == 0 {
		return "", err
	}
	return rooms[0], nil
}

// RoomOnly reads a room's rule.
func (s *Store) RoomOnly(ctx context.Context, room contract.RoomID) ([]string, error) {
	var only string
	if err := s.Read().QueryRowContext(ctx, "SELECT only_hosts FROM rooms WHERE id=?", room).Scan(&only); err != nil {
		return nil, err
	}
	var names []string
	return names, json.Unmarshal([]byte(only), &names)
}

// SetRoomOnly replaces a room's rule. A shared node the rule now reaches serves the room. A room that stops naming a pool stays
// on its nodes' limit, for the boxes it has there: admission takes no new box from it.
func (s *Store) SetRoomOnly(ctx context.Context, room contract.RoomID, only []string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := roomOpen(ctx, tx, room); err != nil {
			return err
		}
		names, _ := json.Marshal(append([]string{}, only...))
		if _, err := tx.ExecContext(ctx, "UPDATE rooms SET only_hosts=? WHERE id=?", string(names), room); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO host_rooms(host,room) SELECT h.name,?1 FROM hosts h, json_each(?2) j WHERE h.pool=j.value AND h.box=''", room, string(names))
		return err
	})
}
