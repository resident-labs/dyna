package worktrees

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const (
	portAlertBandKey       = "worktrees.portAlertBand"
	portAlertGenerationKey = "worktrees.portAlertGeneration"
	portAlertPendingKey    = "worktrees.portAlertPending"
)

// PortCapacityAlert is one upward crossing of the worktree port pool's
// capacity bands. Generation identifies one continuous high-capacity period,
// and Threshold is the crossed percentage of the pool. A crossing stays
// pending until AckPortAlertDelivery confirms its delivery, so a failed post,
// a later escalation or a drop in use never loses it.
type PortCapacityAlert struct {
	Used       int
	Limit      int
	Threshold  int
	Generation int
}

type portAlertState struct {
	used       int
	band       int
	generation int
	pending    []PortCapacityAlert
}

type portAlertReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ObservePortCapacity returns every undelivered 90% or 95% pool crossing,
// oldest first, and records a new crossing when use reaches one. Crossings
// persist in SQLite until acknowledged, so repeated allocations and engine
// restarts do not repeat a delivered alert and never lose an undelivered one.
// At high raw use, the count excludes blocks that allocation can safely reuse.
// Use below 90%, observed at allocation or release, ends the period: the next
// crossing starts a new generation.
func ObservePortCapacity() ([]*PortCapacityAlert, error) {
	db, err := registryDB()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	for {
		current, err := readPortAlertState(ctx, db)
		if err != nil {
			return nil, err
		}
		raw := current.used
		rawBand := portCapacityBand(raw)
		measure := rawBand > current.band || (rawBand > 0 && current.band > 0)
		if rawBand <= current.band && len(current.pending) == 0 && !measure {
			return nil, nil
		}
		var observation portUseObservation
		if measure {
			records, err := loadRecords(ctx, db, "")
			if err == nil {
				// Filesystem, ownership and socket checks stay outside the write
				// transaction. Any record change during measurement retries the step.
				observation = newPortUseObservation(ctx, records)
				if observation.measured {
					current.used = observation.used
				}
			}
			// A registry read error leaves the raw count in place.
		}
		used := current.used
		var pending []*PortCapacityAlert
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			current, err := readPortAlertState(ctx, conn)
			if err != nil {
				return err
			}
			if current.used != raw {
				return errPortCapacityChanged
			}
			if observation.measured {
				matches, err := observedRecordsMatch(ctx, conn, observation)
				if err != nil {
					return err
				}
				if !matches {
					return errPortCapacityChanged
				}
			}
			current.used = used
			if err := applyPortAlertState(ctx, conn, &current); err != nil {
				return err
			}
			pending = alertsOf(current.pending)
			return nil
		})
		if errors.Is(err, errPortCapacityChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return pending, nil
	}
}

var errPortCapacityChanged = errors.New("worktree port capacity changed during observation")

// applyPortAlertState writes one state step: a new crossing is appended to
// the pending list, superseding an undelivered crossing of the same
// threshold, and use below the first band ends the current period.
func applyPortAlertState(ctx context.Context, conn *sql.Conn, current *portAlertState) error {
	band := portCapacityBand(current.used)
	switch {
	case band > current.band:
		if current.band == 0 {
			current.generation++
		}
		current.band = band
		current.pending = supersedePortAlert(current.pending, PortCapacityAlert{Used: current.used, Limit: portSlots, Threshold: band, Generation: current.generation})
		return writePortAlertMeta(ctx, conn, map[string]string{
			portAlertBandKey:       strconv.Itoa(band),
			portAlertGenerationKey: strconv.Itoa(current.generation),
			portAlertPendingKey:    encodePortAlertPending(current.pending),
		})
	case band == 0 && current.band != 0:
		current.band = 0
		return writePortAlertMeta(ctx, conn, map[string]string{portAlertBandKey: "0"})
	}
	return nil
}

// supersedePortAlert replaces an undelivered crossing of the same threshold:
// a room learns the latest facts, and the pending list stays at one crossing
// per threshold.
func supersedePortAlert(pending []PortCapacityAlert, alert PortCapacityAlert) []PortCapacityAlert {
	out := pending[:0]
	for _, p := range pending {
		if p.Threshold != alert.Threshold {
			out = append(out, p)
		}
	}
	return append(out, alert)
}

// AckPortAlertDelivery records that one crossing was delivered to every room.
// The reporter acknowledges after a full sweep succeeds, so a transient
// failure keeps the crossing pending for the next observation.
func AckPortAlertDelivery(generation, threshold int) error {
	db, err := registryDB()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		current, err := readPortAlertState(ctx, conn)
		if err != nil {
			return err
		}
		out := current.pending[:0]
		for _, p := range current.pending {
			if p.Generation != generation || p.Threshold != threshold {
				out = append(out, p)
			}
		}
		if len(out) == len(current.pending) {
			return nil
		}
		return writePortAlertMeta(ctx, conn, map[string]string{portAlertPendingKey: encodePortAlertPending(out)})
	})
}

type portUseObservation struct {
	fingerprint [sha256.Size]byte
	used        int
	measured    bool
}

func newPortUseObservation(ctx context.Context, records []Record) portUseObservation {
	raw := 0
	for _, rec := range records {
		if rec.Port != 0 {
			raw++
		}
	}
	body, err := json.Marshal(records)
	if err != nil {
		return portUseObservation{}
	}
	return portUseObservation{
		fingerprint: sha256.Sum256(body),
		used:        raw - reclaimablePortBlockCount(ctx, records),
		measured:    true,
	}
}

func observedRecordsMatch(ctx context.Context, conn *sql.Conn, observation portUseObservation) (bool, error) {
	records, err := loadRecords(ctx, conn, "")
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(records)
	if err != nil {
		return false, err
	}
	return sha256.Sum256(body) == observation.fingerprint, nil
}

// observePortUseAfterRelease measures the projected post-release count before
// the release takes the SQLite write lock. Its fingerprint covers the exact
// projected record set. An inactive period or a read error skips the scan.
func observePortUseAfterRelease(ctx context.Context, db *sql.DB, releases []portRelease) portUseObservation {
	current, err := readPortAlertState(ctx, db)
	if err != nil || current.band == 0 {
		return portUseObservation{}
	}
	records, err := loadRecords(ctx, db, "")
	if err != nil {
		return portUseObservation{}
	}
	byPath := make(map[string]bool, len(releases))
	for _, release := range releases {
		byPath[release.record.Path] = release.remove
	}
	projected := make([]Record, 0, len(records))
	for _, rec := range records {
		remove, released := byPath[rec.Path]
		if released && remove {
			continue
		}
		if released {
			rec.Port = 0
		}
		projected = append(projected, rec)
	}
	return newPortUseObservation(ctx, projected)
}

// rearmPortCapacity runs inside a transaction that releases a port block.
// Raw use below 90% is always safe. Effective rearming also requires the exact
// measured record set; any concurrent change conservatively skips the rearm.
func rearmPortCapacity(ctx context.Context, conn *sql.Conn, observation portUseObservation) error {
	current, err := readPortAlertState(ctx, conn)
	if err != nil {
		return err
	}
	if current.band == 0 {
		return nil
	}
	rearm := portCapacityBand(current.used) == 0
	if !rearm && observation.measured && portCapacityBand(observation.used) == 0 {
		matches, err := observedRecordsMatch(ctx, conn, observation)
		rearm = err == nil && matches
	}
	if !rearm {
		return nil
	}
	return writePortAlertMeta(ctx, conn, map[string]string{portAlertBandKey: "0"})
}

func encodePortAlertPending(pending []PortCapacityAlert) string {
	if len(pending) == 0 {
		return "[]"
	}
	body, err := json.Marshal(pending)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func alertsOf(pending []PortCapacityAlert) []*PortCapacityAlert {
	out := make([]*PortCapacityAlert, len(pending))
	for i := range pending {
		out[i] = &pending[i]
	}
	return out
}

func writePortAlertMeta(ctx context.Context, conn *sql.Conn, values map[string]string) error {
	for key, value := range values {
		if _, err := conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	return nil
}

// portCapacityBand returns the highest alert threshold that used reaches.
func portCapacityBand(used int) int {
	switch {
	case used >= portSlots*19/20:
		return 95
	case used >= portSlots*9/10:
		return 90
	default:
		return 0
	}
}

func readPortAlertState(ctx context.Context, q portAlertReader) (portAlertState, error) {
	var state portAlertState
	var band, generation, pending string
	err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM worktree_records WHERE COALESCE(json_extract(body,'$.port'),0) <> 0), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'[]')", portAlertBandKey, portAlertGenerationKey, portAlertPendingKey).Scan(&state.used, &band, &generation, &pending)
	if err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port capacity: %w", err)
	}
	if state.band, err = strconv.Atoi(band); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert band: %w", err)
	}
	if state.generation, err = strconv.Atoi(generation); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert generation: %w", err)
	}
	if err := json.Unmarshal([]byte(pending), &state.pending); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert pending: %w", err)
	}
	return state, nil
}
