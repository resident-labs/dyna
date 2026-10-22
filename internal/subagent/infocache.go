package subagent

import (
	"dyna-agent/internal/sessionlife"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// All metadata readers share parsed snapshots. The display paths also keep
// a brief turn summary, keyed by its turn row's revision.

type turnBrief struct {
	n       int
	status  string
	done    bool
	pending bool
}

type infoEntry struct {
	metaKey sessionlife.Version
	meta    Meta
	turnKey string
	turn    turnBrief
	hasTurn bool
	raw     []byte
	err     error
}

// infoCache owns parsed session snapshots and the display turn summary.
var infoCache sync.Map
var metaMu sync.Mutex

// recordKey names session id's record in infoCache.
func (s *Store) recordKey(id string) string { return filepath.Join(s.root(), id) }

// metaSnapshot checks identity on every read, including locked updates and
// closed sessions. Atomic writes change the record version even at the same
// size and time.
func (s *Store) metaSnapshot(id string) (*infoEntry, error) {
	if _, err := s.Dir(id); err != nil {
		return nil, err
	}
	metaMu.Lock()
	defer metaMu.Unlock()
	cacheKey := s.recordKey(id)
	key, ok := s.recordVersion(id)
	if !ok {
		infoCache.Delete(cacheKey)
		// Preserve the normal read error, including permission failures.
	}
	if ok {
		if value, hit := infoCache.Load(cacheKey); hit {
			entry := value.(*infoEntry)
			if key != "" && entry.metaKey == key {
				return entry, entry.err
			}
		}
	}
	// The version comes from the record that was read, not one replaced during the read.
	data, key, err := sessionlife.LoadRaw(s.root(), id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%w %q", ErrNotFound, id)
		}
		return nil, err
	}
	entry := &infoEntry{metaKey: key, raw: data}
	var record struct {
		Meta
		// TODO: legacy/shim remove later — passes old done records to the shared lifecycle snapshot; remove when no retained session.json has done.
		Done json.RawMessage `json:"done"`
	}
	decode := s.decodeMeta
	if decode == nil {
		decode = json.Unmarshal
	}
	if err := decode(data, &record); err != nil {
		entry.err = fmt.Errorf("parse session %s: %w", id, err)
	} else {
		entry.meta = record.Meta
		lifecycle := sessionlife.Snapshot{Closed: entry.meta.Closed, LegacyDone: record.Done}
		if !entry.meta.Closed.IsZero() && !lifecycle.IsClosed() {
			entry.meta.Finished, entry.meta.Closed = entry.meta.Closed, time.Time{}
		}
		if !entry.meta.Closed.IsZero() {
			entry.meta.Status = StatusClosed
		} else if entry.meta.Status == "" {
			// TODO: legacy/shim remove later — old metadata stores dormancy only in Finished; remove when all session records have status.
			entry.meta.Status = StatusIdle
			if !entry.meta.Finished.IsZero() {
				entry.meta.Status = StatusDormant
			}
		}
	}
	infoCache.Store(cacheKey, entry)
	return entry, entry.err
}

// cachedMeta and ReadMeta share the same snapshot owner. ReadMeta returns a
// copy and resolves inherited service roles against current parent snapshots.
func (s *Store) cachedMeta(id string) (Meta, *infoEntry, error) {
	m, err := s.ReadMeta(id)
	if err != nil {
		return Meta{}, nil, err
	}
	value, _ := infoCache.Load(s.recordKey(id))
	if value == nil {
		return m, nil, nil
	}
	return m, value.(*infoEntry), nil
}

// cachedTurn is the status of turn n, re-read only when its row changes.
func (s *Store) cachedTurn(id string, n int, e *infoEntry) (turnBrief, error) {
	key, ok := s.turnVersion(id, n)
	if ok && e != nil && e.hasTurn && e.turn.n == n && e.turnKey == key {
		return e.turn, nil
	}
	t, err := s.ReadTurn(id, n)
	if err != nil {
		return turnBrief{}, err
	}
	b := turnBrief{n: t.N, status: t.Status, done: t.Done(), pending: t.RoomHeld || t.HeldPrompt != "" && !t.HeldAccepted}
	if ok && e != nil {
		// Publish a fresh entry: readers hold the old one without a lock.
		next := *e
		next.turnKey, next.turn, next.hasTurn = key, b, true
		infoCache.CompareAndSwap(s.recordKey(id), e, &next)
	}
	return b, nil
}

func cloneValue[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

func cloneConsolidation(c ConsolidationState) ConsolidationState {
	c.Sessions = slices.Clone(c.Sessions)
	c.Sources = slices.Clone(c.Sources)
	for i := range c.Sources {
		c.Sources[i].PendingIDs = slices.Clone(c.Sources[i].PendingIDs)
		c.Sources[i].Reservation = cloneValue(c.Sources[i].Reservation)
	}
	return c
}

func cloneMeta(m Meta) Meta {
	m.InheritedReceipts = maps.Clone(m.InheritedReceipts)
	m.ClosedPosts = maps.Clone(m.ClosedPosts)
	m.ConsolidationHistory = slices.Clone(m.ConsolidationHistory)
	for i := range m.ConsolidationHistory {
		m.ConsolidationHistory[i] = cloneConsolidation(m.ConsolidationHistory[i])
	}
	m.Transfer = cloneValue(m.Transfer)
	if m.Consolidation != nil {
		c := cloneConsolidation(*m.Consolidation)
		m.Consolidation = &c
	}
	m.InheritedInboxes = slices.Clone(m.InheritedInboxes)
	for i := range m.InheritedInboxes {
		m.InheritedInboxes[i].PendingIDs = slices.Clone(m.InheritedInboxes[i].PendingIDs)
	}
	m.Closing = cloneValue(m.Closing)
	m.RuntimePin = cloneValue(m.RuntimePin)
	m.Instructions = m.Instructions.Clone()
	m.InstructionsKnown = m.InstructionsKnown.Clone()
	m.InstructionsNotes = slices.Clone(m.InstructionsNotes)
	m.UsageBaseline = cloneValue(m.UsageBaseline)
	if m.UsageBaseline != nil {
		m.UsageBaseline.Input = cloneValue(m.UsageBaseline.Input)
		m.UsageBaseline.CachedInput = cloneValue(m.UsageBaseline.CachedInput)
		m.UsageBaseline.Output = cloneValue(m.UsageBaseline.Output)
	}
	return m
}
