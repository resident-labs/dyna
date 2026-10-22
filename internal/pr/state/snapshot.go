package state

import "reflect"

// Snapshot holds one committed PR revision. Its state is private and immutable.
type Snapshot struct {
	state    State
	revision string
}

// Revision is the root-scoped committed revision, or "missing" for an empty store.
func (s Snapshot) Revision() string { return s.revision }

// Copy returns an independent mutable copy for building a projection or update.
func (s Snapshot) Copy() State { return copyForRead(s.state) }

// Metadata returns isolated index-level state without records or HTTP responses.
// Callers can combine it with Record without copying every PR's history.
func (s Snapshot) Metadata() State {
	metadata := s.state
	metadata.Records, metadata.Responses = nil, nil
	return cloneState(metadata)
}

// ChangedKeys returns keys whose stored row or derived quota differs from the
// previous snapshot. Removed records are included. A zero previous snapshot
// selects every current record. It never exposes shared snapshot memory.
func (s Snapshot) ChangedKeys(previous Snapshot) []string {
	var keys []string
	for key, record := range s.state.Records {
		if record != previous.state.Records[key] {
			keys = append(keys, key)
		}
	}
	for key := range previous.state.Records {
		if _, found := s.state.Records[key]; !found {
			keys = append(keys, key)
		}
	}
	return keys
}

// HasRecord reports whether key is present, without resolving change ID aliases.
func (s Snapshot) HasRecord(key string) bool { return s.state.Records[key] != nil }

// Record returns an isolated copy of one record, by key or change ID.
func (s Snapshot) Record(id string) (Record, bool) {
	r := s.state.RecordByID(id)
	if r == nil {
		return Record{}, false
	}
	return *copyForRecord(r), true
}

// Responses returns an isolated copy of one record's HTTP response entries.
func (s Snapshot) Responses(id string) map[string]CachedResponse {
	r := s.state.RecordByID(id)
	if r == nil {
		return nil
	}
	entries := s.state.Responses[r.Key]
	return copyResponses(entries)
}

// copyForRecord is the isolated record copier shared by reads and scoped writes.
// Tests count real row copies here, including nested review and notice data.
var copyForRecord = func(record *Record) *Record {
	return cloneStateValue(reflect.ValueOf(record)).Interface().(*Record)
}

func copyResponses(entries map[string]CachedResponse) map[string]CachedResponse {
	return cloneStateValue(reflect.ValueOf(entries)).Interface().(map[string]CachedResponse)
}

// RecordByID finds a record in a caller-owned mutable state.
func (st State) RecordByID(id string) *Record {
	if r := st.Records[id]; r != nil {
		return r
	}
	for _, r := range st.Records {
		if r != nil && r.ChangeID == id {
			return r
		}
	}
	return nil
}
