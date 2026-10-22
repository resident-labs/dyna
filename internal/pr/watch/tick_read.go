package watch

import (
	"context"
	"maps"
	"sync"

	"dyna-agent/internal/pr/state"
)

// copyTickRecord is the real isolated row reader; tests count copies through it.
var copyTickRecord = state.Snapshot.Record

// tickReadView owns its record and response copies. It refreshes them from the
// one committed snapshot source. Helpers only inspect these rows; writes still
// use Store.update or Store.updateRecords, with separate mutable inputs.
// Each observation gets fresh metadata and outer maps, so workers can keep
// an older observation while later phases refresh changed rows.
type tickReadView struct {
	mu        sync.Mutex
	root      string
	snapshot  state.Snapshot
	records   map[string]*Record
	responses map[string]map[string]cachedResponse
}

type tickReadKey struct{}

type tickReadScope struct {
	service *Service
	view    *tickReadView
}

// Services can be copied when restarting a watcher. Keep the cursor behind a
// pointer, and serialize its first initialization without copying a mutex.
var tickReadInit sync.Mutex

func (s *Service) tickView() *tickReadView {
	tickReadInit.Lock()
	defer tickReadInit.Unlock()
	if s.tickReads == nil {
		s.tickReads = &tickReadView{}
	}
	return s.tickReads
}

func (v *tickReadView) read(root string) (State, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	snapshot, err := state.ReadSnapshot(root)
	if err != nil {
		return State{}, err
	}
	if v.root != root {
		v.snapshot, v.records, v.responses = state.Snapshot{}, nil, nil
	}
	st := snapshot.Metadata()
	st.Records, st.Responses = maps.Clone(v.records), maps.Clone(v.responses)
	if st.Records == nil {
		st.Records = map[string]*Record{}
		st.Responses = map[string]map[string]cachedResponse{}
	}
	for _, key := range snapshot.ChangedKeys(v.snapshot) {
		if !snapshot.HasRecord(key) {
			delete(st.Records, key)
			delete(st.Responses, key)
			continue
		}
		record, _ := copyTickRecord(snapshot, key)
		st.Records[key] = &record
		if responses := snapshot.Responses(key); responses != nil {
			st.Responses[key] = responses
		} else {
			delete(st.Responses, key)
		}
	}
	v.root, v.snapshot = root, snapshot
	v.records, v.responses = maps.Clone(st.Records), maps.Clone(st.Responses)
	return st, nil
}

// readTickState is restricted to read-only helpers in a Tick. Other service
// calls retain Store.Read's fully independent mutable result.
func (s *Service) readTickState(ctx context.Context) (State, error) {
	if scope, _ := ctx.Value(tickReadKey{}).(tickReadScope); scope.service == s {
		return scope.view.read(s.Store.Root)
	}
	return s.Store.Read()
}
