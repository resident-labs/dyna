package roomsvc

import (
	"encoding/json"
	"maps"
	"strings"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/listchange"
)

// goalScan publishes goal changes to the room watcher. It reads goals only
// through its service. The caller keeps the signature, work and version
// snapshots, so every scan starts from the state the caller passed in.
type goalScan struct {
	service *goalsvc.Service
}

func (g goalScan) run(now time.Time, signatures, work map[string]string, version *string, first bool, emit func(Event)) {
	service := g.service
	service.Store.Now = func() time.Time { return now }
	current, err := service.Store.Revision()
	// Expiry changes the published goal even without a database write.
	const expiryKey = "\x00expediteUntil"
	const expiryPrefix = expiryKey + "\x00"
	const revisionPrefix = "\x00roomRevision\x00"
	expiry, _ := time.Parse(time.RFC3339Nano, work[expiryKey])
	if err != nil || current == *version && (expiry.IsZero() || now.Before(expiry)) {
		return
	}
	revisions, err := service.Store.RoomRevisions()
	if err != nil {
		return
	}
	all := first || *version == "" || work[revisionPrefix+goals.Global] != revisions[goals.Global]
	selected := map[string]bool{}
	for roomID, revision := range revisions {
		if roomID != goals.Global && (all || work[revisionPrefix+roomID] != revision) {
			selected[roomID] = true
		}
	}
	for key, value := range work {
		if strings.HasPrefix(key, revisionPrefix) {
			roomID := strings.TrimPrefix(key, revisionPrefix)
			if roomID != goals.Global {
				if _, exists := revisions[roomID]; all || !exists {
					selected[roomID] = true
				}
			}
		}
		if strings.HasPrefix(key, expiryPrefix) {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err == nil && !now.Before(at) {
				selected[strings.TrimPrefix(key, expiryPrefix)] = true
			}
		}
	}
	var state goals.State
	if all {
		state, err = service.Store.Read()
	} else {
		rooms := make([]string, 0, len(selected))
		for roomID := range selected {
			rooms = append(rooms, roomID)
		}
		state, err = service.Store.ReadRooms(rooms)
	}
	if err != nil {
		return
	}
	// Prepare every input before advancing any caller-owned snapshot.
	nextWork := maps.Clone(work)
	for roomID := range selected {
		delete(nextWork, roomID)
		delete(nextWork, "rules\x00"+roomID)
		delete(nextWork, expiryPrefix+roomID)
		delete(nextWork, revisionPrefix+roomID)
	}
	rows := []listchange.Item{}
	present := map[string]bool{}
	wake, metadata := map[string]bool{}, map[string]bool{}
	for roomID, room := range state.Rooms {
		selected[roomID] = true
		items := make([]goals.Goal, 0, len(room.Goals))
		inputs := map[string]goals.Goal{}
		var roomExpiry time.Time
		for id, goal := range room.Goals {
			if goal.ExpeditedAt(now) && (roomExpiry.IsZero() || goal.ExpediteUntil.Before(roomExpiry)) {
				roomExpiry = *goal.ExpediteUntil
			}
			items = append(items, *goal)
			inputs[id] = *goal
			present[listchange.Key("goals", roomID, id)] = true
		}
		page, err := goalPage(items, "", 0)
		if err != nil {
			return
		}
		id := func(g goals.Goal) string { return g.ID }
		rows = append(rows, listchange.Rows("goals", roomID, items, listchange.HeadIDs(page.Items, id), id)...)
		data, err := json.Marshal(inputs)
		if err != nil {
			return
		}
		wake[roomID] = work[roomID] != string(data)
		nextWork[roomID] = string(data)
		rules, err := json.Marshal(struct {
			Global *goals.RuleSet
			Room   goals.RuleSet
		}{state.Global, room.RuleSet})
		if err != nil {
			return
		}
		key := "rules\x00" + roomID
		metadata[roomID] = work[key] != string(rules)
		nextWork[key] = string(rules)
		if !roomExpiry.IsZero() {
			nextWork[expiryPrefix+roomID] = roomExpiry.Format(time.RFC3339Nano)
		}
	}
	var gone []string
	for key := range signatures {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) == 3 && selected[parts[1]] && !present[key] {
			gone = append(gone, key)
			wake[parts[1]] = true
		}
	}
	var changes []listchange.Event
	if first {
		changes, err = listchange.Sync(signatures, rows, true)
	} else {
		changes, err = listchange.Patch(signatures, rows, gone)
	}
	if err != nil {
		return
	}
	delete(nextWork, revisionPrefix+goals.Global)
	for roomID, revision := range revisions {
		nextWork[revisionPrefix+roomID] = revision
	}
	delete(nextWork, expiryKey)
	var nextExpiry time.Time
	for key, value := range nextWork {
		if strings.HasPrefix(key, expiryPrefix) {
			at, _ := time.Parse(time.RFC3339Nano, value)
			if !at.IsZero() && (nextExpiry.IsZero() || at.Before(nextExpiry)) {
				nextExpiry = at
			}
		}
	}
	if !nextExpiry.IsZero() {
		nextWork[expiryKey] = nextExpiry.Format(time.RFC3339Nano)
	}
	clear(work)
	maps.Copy(work, nextWork)
	*version = current
	emitted := map[string]bool{}
	for _, change := range changes {
		value := change.Change
		emit(Event{Kind: "goals", RoomID: change.RoomID, Change: &value, WorkerWake: wake[change.RoomID]})
		wake[change.RoomID] = false
		emitted[change.RoomID] = true
	}
	if !first {
		for roomID, changed := range metadata {
			if changed && !emitted[roomID] {
				emit(Event{Kind: "goals", RoomID: roomID, WorkerWake: wake[roomID]})
			}
		}
	}
}
