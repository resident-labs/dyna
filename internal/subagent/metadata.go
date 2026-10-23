package subagent

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// ListMeta reads session metadata without repairing turns or running report
// callbacks. Unassigned legacy history and removed records are skipped.
// Damaged metadata still stops the scan, so destructive callers keep evidence.
func (s *Store) ListMeta() ([]Meta, error) { return s.listMeta(true, false) }

// ListOpenMeta skips closed sessions from their cached metadata, before the
// full read, so its cost does not grow with closed history.
func (s *Store) ListOpenMeta() ([]Meta, error) { return s.listMeta(true, true) }

func (s *Store) listMeta(strict, open bool) ([]Meta, error) {
	s, ids, err := s.listing(open)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, id := range ids {
		if open && s.cachedClosed(id) {
			continue
		}
		beforeRoomMetaRead(id)
		meta, assigned, err := s.readRoomMeta(id)
		if errors.Is(err, ErrNotFound) || os.IsNotExist(err) {
			continue
		}
		if err != nil {
			if strict {
				return nil, fmt.Errorf("session %s (unreadable: %w)", id, err)
			}
			continue
		}
		// The session can close between the cached check and this read.
		if assigned && (!open || meta.Closed.IsZero()) {
			out = append(out, meta)
		}
	}
	return out, nil
}

// roomMeta shares room filtering for strict controls and tolerant delivery.
func (s *Store) roomMeta(roomID string, strict bool) ([]Meta, error) {
	all, err := s.listMeta(strict, true)
	if err != nil {
		return nil, err
	}
	out := []Meta{}
	for _, meta := range all {
		if meta.RoomID == roomID {
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address() < out[j].Address() })
	return out, nil
}

// beforeRoomMetaRead lets a test close a session between the two reads.
var beforeRoomMetaRead = func(string) {}

// cachedClosed reads only the cached snapshot. Unreadable records are not
// closed, so the full read still reports them.
func (s *Store) cachedClosed(id string) bool {
	entry, err := s.metaSnapshot(id)
	return err == nil && !entry.meta.Closed.IsZero()
}

// readRoomMeta distinguishes unassigned history from damaged room evidence.
// It never invents membership or rewrites the original record.
func (s *Store) readRoomMeta(id string) (Meta, bool, error) {
	meta, _, err := s.readMetaRecord(id)
	if err != nil {
		return Meta{}, false, err
	}
	if meta.ID != id {
		return Meta{}, false, fmt.Errorf("session %s has missing or mismatched metadata ID", id)
	}
	if meta.HistoryOnly {
		return meta, false, nil
	}
	if meta.RoomID != "" && meta.Room != "" {
		return meta, true, nil
	}
	return Meta{}, false, fmt.Errorf("%w: session %s has an incomplete room stamp", ErrNoRoom, id)
}
