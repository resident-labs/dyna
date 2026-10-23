package health

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/store"
)

// oldBinaryWrites writes a profile's record the way the previous release
// does: a new file renamed onto its path.
func oldBinaryWrites(t *testing.T, s *Service, name string, r record) {
	t.Helper()
	path, err := s.path(name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, json.RawMessage(b), 0o600); err != nil {
		t.Fatal(err)
	}
}
