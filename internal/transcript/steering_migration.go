package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

const steeringAnchorsKey = "transcript.steering_anchors"

// MigrateSteeringAnchors stores, once, the source offset where each retained
// turn's transcript showed its delivered steering. It reads turns and their
// canonical output before the versioned migration writes, so no artifact is
// read inside the SQL writer. A turn whose output is missing keeps unknown
// anchors. It runs after subagent.Migrate and runstore.Migrate.
func MigrateSteeringAnchors(dataDir string) error {
	return migrateSteeringAnchors(dataDir, nil)
}

func migrateSteeringAnchors(dataDir string, preparedHook func()) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	// Only the current version skips preparation; database.Migrate still
	// refuses a newer one.
	var version string
	if db.QueryRow("SELECT value FROM meta WHERE key=?", steeringAnchorsKey).Scan(&version) == nil && version == "1" {
		return nil
	}
	ctx := context.Background()
	sessions := &subagent.Store{Root: filepath.Join(dataDir, "sessions")}
	type prepared struct {
		id      string
		snap    subagent.SteeringAnchorSnapshot
		anchors []*int64
	}
	var work []prepared
	entries, err := os.ReadDir(sessions.Root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !runstore.IsSessionDirID(id) {
			continue
		}
		snaps, err := sessions.SteeringAnchorSnapshots(ctx, id)
		if err != nil {
			return fmt.Errorf("session %s steering: %w", id, err)
		}
		for _, snap := range snaps {
			if !needsAnchors(snap.Turn) {
				continue
			}
			events, end, ok, err := turnEvents(dataDir, id, snap.Turn)
			if err != nil {
				return fmt.Errorf("session %s turn %d output: %w", id, snap.Turn.N, err)
			}
			if ok {
				work = append(work, prepared{id: id, snap: snap, anchors: HistoricalAnchors(snap.Turn, events, end)})
			}
		}
	}
	if preparedHook != nil {
		preparedHook()
	}
	return database.Migrate(db, steeringAnchorsKey, 1, func(conn *sql.Conn, _ int) error {
		for _, w := range work {
			if err := sessions.SetSteeringAnchors(ctx, conn, w.id, w.snap, w.anchors); err != nil {
				return fmt.Errorf("session %s turn %d steering anchors: %w", w.id, w.snap.Turn.N, err)
			}
		}
		return nil
	})
}

// needsAnchors reports a turn with steering a reader can place but that has
// no anchor yet.
func needsAnchors(t subagent.Turn) bool {
	for _, s := range t.Steering {
		if s.SourceOffset == nil && (s.Delivered || t.Done()) {
			return true
		}
	}
	return false
}

// turnEvents reads a session turn's canonical events with their source
// offsets, and the end of its complete output prefix. ok is false when the
// output file holds no evidence for the turn.
func turnEvents(dataDir, id string, t subagent.Turn) (events []workeroutput.Event, end int64, ok bool, err error) {
	key := filepath.Join("sessions", id, "agents", "1")
	path := filepath.Join(dataDir, key, "output.jsonl")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if t.NormStart > info.Size() || t.NormEnd > info.Size() {
		return nil, 0, false, nil
	}
	// The turn's range ends at its recorded end, else at the size captured
	// above, so a growing file is never read past it. A zero limit reads to
	// the end, so an empty range is not read at all.
	rangeEnd := info.Size()
	if t.NormEnd > 0 {
		rangeEnd = t.NormEnd
	}
	limit := rangeEnd - t.NormStart
	if limit <= 0 {
		return nil, t.NormStart, true, nil
	}
	reader, err := projection.Open(dataDir, filepath.ToSlash(key), path)
	if err != nil {
		return nil, 0, false, err
	}
	defer reader.Close()
	next, err := runstore.VisitNormalizedOutputLines(path, t.NormStart, limit, func(line projection.Line) error {
		e, converted, err := reader.Event(line.Start, line.End, line.Raw)
		if err != nil {
			return err
		}
		if !converted && json.Unmarshal(line.Raw, &e) != nil {
			return nil
		}
		e.SourceOffset = line.Start
		events = append(events, e)
		return nil
	})
	if err != nil {
		return nil, 0, false, err
	}
	// The turn end is the last complete line the visitor proved, never an
	// unchecked range end inside a partial line.
	return events, next, true, nil
}

// TODO: legacy/shim remove later — converts historical timestamp placement to
// original output boundaries once, after output projection migration.
func HistoricalAnchors(t subagent.Turn, events []workeroutput.Event, end int64) []*int64 {
	items, _, conts := fromEvents(events)
	timed := false
	for _, item := range items {
		if !item.TS.IsZero() {
			timed = true
			break
		}
	}
	var shown []int
	for i, s := range t.Steering {
		if s.Message != "" && strings.Contains(t.Prompt, s.Message) {
			continue
		}
		shown = append(shown, i)
	}
	sort.SliceStable(shown, func(i, j int) bool { return t.Steering[shown[i]].TS < t.Steering[shown[j]].TS })
	anchors := make([]*int64, len(t.Steering))
	for i, k := range shown {
		s := t.Steering[k]
		if !s.Delivered && !t.Done() {
			continue
		}
		pos := len(items)
		if timed {
			for j, item := range items {
				if !item.TS.IsZero() && item.TS.After(time.UnixMilli(s.TS)) {
					pos = j
					break
				}
			}
		} else if i < len(conts) {
			pos = conts[i]
		}
		anchor := end
		if pos < len(items) {
			anchor = items[pos].source
		}
		anchors[k] = &anchor
	}
	return anchors
}
