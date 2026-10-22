// Package listchange compares API rows and emits one changed item at a time.
package listchange

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
)

type Change struct {
	Op             string          `json:"op"`
	ID             string          `json:"id"`
	Item           json.RawMessage `json:"item,omitempty"`
	InHead         *bool           `json:"inHead,omitempty"`
	Controls       bool            `json:"controls,omitempty"`
	Consolidations bool            `json:"consolidations,omitempty"`
}

type Item struct {
	Kind   string
	RoomID string
	ID     string
	Value  any
	InHead *bool
	// Version, when set, replaces the row's encoding in the comparison, so a
	// row can leave out fields that change without new content.
	Version string
}

type Event struct {
	Kind   string
	RoomID string
	Change Change
}

func HeadIDs[T any](rows []T, id func(T) string) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[id(row)] = true
	}
	return out
}

// Rows leaves head membership absent for unpaged lists. An empty head map
// marks every supplied row as history outside the default first page.
func Rows[T any](kind, roomID string, rows []T, head map[string]bool, id func(T) string) []Item {
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := Item{Kind: kind, RoomID: roomID, ID: id(row), Value: row}
		if head != nil {
			inHead := head[item.ID]
			item.InHead = &inHead
		}
		out = append(out, item)
	}
	return out
}

// Sync retains serialized rows for comparison. A failed encoding leaves the
// previous snapshot intact, so a later scan can still report every change.
func Sync(previous map[string]string, rows []Item, first bool) ([]Event, error) {
	next, events, err := compare(previous, rows, first)
	if err != nil {
		return nil, err
	}
	for key := range previous {
		if _, ok := next[key]; !ok && !first {
			parts := strings.SplitN(key, "\x00", 3)
			events = append(events, Event{Kind: parts[0], RoomID: parts[1], Change: Change{Op: "delete", ID: parts[2]}})
		}
	}
	clear(previous)
	maps.Copy(previous, next)
	return sorted(events), nil
}

// Patch is Update that also reports each key in gone (keyed as Sync keys
// rows) deleted, and drops it from the snapshot.
func Patch(previous map[string]string, rows []Item, gone []string) ([]Event, error) {
	next, events, err := compare(previous, rows, false)
	if err != nil {
		return nil, err
	}
	for _, key := range gone {
		parts := strings.SplitN(key, "\x00", 3)
		if _, ok := previous[key]; !ok || len(parts) != 3 {
			continue
		}
		events = append(events, Event{Kind: parts[0], RoomID: parts[1], Change: Change{Op: "delete", ID: parts[2]}})
		delete(previous, key)
	}
	maps.Copy(previous, next)
	return sorted(events), nil
}

// Key is how Sync keys a row in its snapshot.
func Key(kind, roomID, id string) string { return kind + "\x00" + roomID + "\x00" + id }

// headMark is how a versioned row's snapshot records its list-head place.
func headMark(inHead bool) string { return fmt.Sprint("\x00", inHead) }

// HeadChanged reports whether a row's snapshot, as Sync stored it, records
// a list-head place other than inHead, or none.
func HeadChanged(snapshot string, inHead bool) bool {
	return !strings.HasSuffix(snapshot, headMark(inHead))
}

// Update is Sync for some rows only: rows it is not given keep their
// snapshot and are not reported deleted.
func Update(previous map[string]string, rows []Item) ([]Event, error) {
	next, events, err := compare(previous, rows, false)
	if err != nil {
		return nil, err
	}
	maps.Copy(previous, next)
	return sorted(events), nil
}

// compare encodes rows and returns them by key, with an upsert for each one
// that differs from previous.
func compare(previous map[string]string, rows []Item, first bool) (map[string]string, []Event, error) {
	next := make(map[string]string, len(rows))
	events := []Event{}
	for _, row := range rows {
		key := Key(row.Kind, row.RoomID, row.ID)
		if row.Version != "" {
			next[key] = row.Version
			if row.InHead != nil {
				next[key] += headMark(*row.InHead)
			}
			// A row with its version unchanged sends nothing, so it needs no
			// encoding: most rows of a large list are unchanged on each pass.
			if first || previous[key] == next[key] {
				continue
			}
		}
		data, err := json.Marshal(row.Value)
		if err != nil {
			return nil, nil, err
		}
		change := Change{Op: "upsert", ID: row.ID, Item: data, InHead: row.InHead}
		if row.Version == "" {
			encoded, err := json.Marshal(change)
			if err != nil {
				return nil, nil, err
			}
			next[key] = string(encoded)
		}
		if !first && previous[key] != next[key] {
			events = append(events, Event{Kind: row.Kind, RoomID: row.RoomID, Change: change})
		}
	}
	return next, events, nil
}

func sorted(events []Event) []Event {
	sort.Slice(events, func(i, j int) bool {
		if events[i].RoomID != events[j].RoomID {
			return events[i].RoomID < events[j].RoomID
		}
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].Change.ID < events[j].Change.ID
	})
	return events
}
