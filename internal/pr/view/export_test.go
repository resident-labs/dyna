package view

import (
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

// CountRows counts the rows Scan builds until the test ends.
func CountRows(t testing.TB) *int {
	n := new(int)
	old := buildRow
	buildRow = func(facts watch.ReviewFacts, r watch.Record) ChangeView { *n++; return old(facts, r) }
	t.Cleanup(func() { buildRow = old })
	return n
}

// CountProjectionCopies counts actual copies made for request/watch projections.
func CountProjectionCopies(t *testing.T) *int {
	n := new(int)
	old := copyProjectionRecord
	copyProjectionRecord = func(snapshot state.Snapshot, key string) (watch.Record, bool) {
		record, ok := old(snapshot, key)
		if ok {
			*n++
		}
		return record, ok
	}
	t.Cleanup(func() { copyProjectionRecord = old })
	return n
}

// CountChangedKeyQueries counts actual delta queries until the test ends.
func CountChangedKeyQueries(t *testing.T) *int {
	n := new(int)
	read := keysChangedSince
	keysChangedSince = func(root, since string) ([]string, error) {
		*n++
		return read(root, since)
	}
	t.Cleanup(func() { keysChangedSince = read })
	return n
}

// CountProjectionFacts counts actual live reviewer observations.
func CountProjectionFacts(t testing.TB) *int {
	n := new(int)
	read := projectionFacts
	projectionFacts = func(service *watch.Service, st watch.State) (watch.ReviewFacts, error) {
		*n++
		return read(service, st)
	}
	t.Cleanup(func() { projectionFacts = read })
	return n
}

// CountWorkerInputJoins counts actual whole-room worker-input construction.
func CountWorkerInputJoins(t *testing.T) *int {
	n := new(int)
	join := scanWorkerInputs
	scanWorkerInputs = func(st watch.State, room string, records []*watch.Record, memo map[string]string, stale func(string) bool) string {
		*n++
		return join(st, room, records, memo, stale)
	}
	t.Cleanup(func() { scanWorkerInputs = join })
	return n
}
