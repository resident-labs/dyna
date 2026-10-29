package state

import (
	"context"
	"fmt"
)

// UpdateRecords runs fn with isolated metadata and only the declared record and
// response keys. Missing declared records may be added; removing a declared
// record also removes its responses. Other rows remain unchanged, apart from
// the writer's derived quota display and normal closed-record pruning.
// An undeclared record or response key aborts the update without publishing.
func UpdateRecords(ctx context.Context, root string, keys []string, fn func(*State) error) error {
	return updateSelectedRecords(ctx, root, func(Snapshot) []string { return keys }, fn)
}

// RecordScope contains only scalar fields for selecting mutable update rows.
type RecordScope struct {
	Key, RoomID  string
	Watching     bool
	State        PRState
	HasResponses bool
}

// UpdateSelectedRecords selects rows inside the writer lock, then isolates them.
// As with UpdateRecords, fn must not retain the mutable state after it returns.
func UpdateSelectedRecords(ctx context.Context, root string, selectRow func(RecordScope) bool, fn func(*State) error) error {
	return updateSelectedRecords(ctx, root, func(snapshot Snapshot) []string {
		var keys []string
		for key, record := range snapshot.state.Records {
			if selectRow(RecordScope{Key: key, RoomID: record.RoomID, Watching: record.Watching, State: record.State, HasResponses: len(snapshot.state.Responses[key]) > 0}) {
				keys = append(keys, key)
			}
		}
		return keys
	}, fn)
}

func updateSelectedRecords(ctx context.Context, root string, selectKeys func(Snapshot) []string, fn func(*State) error) error {
	return update(ctx, root, func(snapshot Snapshot) (State, error) {
		keys := selectKeys(snapshot)
		declared := make(map[string]bool, len(keys))
		for _, key := range keys {
			declared[key] = true
		}
		scoped := snapshot.Metadata()
		scoped.Records = make(map[string]*Record, len(declared))
		scoped.Responses = make(map[string]map[string]CachedResponse, len(declared))
		for key := range declared {
			if record := snapshot.state.Records[key]; record != nil {
				scoped.Records[key] = copyForRecord(record)
			}
			if entries := snapshot.state.Responses[key]; entries != nil {
				scoped.Responses[key] = copyResponses(entries)
			}
		}
		if err := fn(&scoped); err != nil {
			return State{}, err
		}
		for key := range scoped.Records {
			if !declared[key] {
				return State{}, fmt.Errorf("undeclared PR record key %q", key)
			}
		}
		for key := range scoped.Responses {
			if !declared[key] {
				return State{}, fmt.Errorf("undeclared PR response key %q", key)
			}
		}

		merged := scoped
		merged.Records = make(map[string]*Record, len(snapshot.state.Records)+len(scoped.Records))
		merged.Responses = make(map[string]map[string]CachedResponse, len(snapshot.state.Responses)+len(scoped.Responses))
		merged.unchanged = make(map[string]*Record, len(snapshot.state.Records))
		for key, record := range snapshot.state.Records {
			// normalizeState changes only scalar fields on untouched rows. Copy the
			// record itself so quota refresh cannot change a retained snapshot.
			copy := *record
			merged.Records[key] = &copy
			merged.unchanged[key] = record
		}
		for key, entries := range snapshot.state.Responses {
			merged.Responses[key] = entries
		}
		for key := range declared {
			delete(merged.unchanged, key)
			if record := scoped.Records[key]; record != nil {
				merged.Records[key] = record
				if entries, found := scoped.Responses[key]; found {
					merged.Responses[key] = entries
				} else {
					delete(merged.Responses, key)
				}
			} else {
				delete(merged.Records, key)
				delete(merged.Responses, key)
			}
		}
		return merged, nil
	})
}
