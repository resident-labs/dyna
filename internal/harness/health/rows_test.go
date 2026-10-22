package health

import (
	"bytes"
	"encoding/json"
	"os"
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

// The copy is rewritten only when a field that decides the status changes:
// a later check with the same outcome moves the row's times, not the file.
func TestCopyIsRewrittenOnlyWhenTheStatusInputsChange(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	path, _ := s.path(p.Name)
	recordTurn(t, s, p, "v1", observation(1))
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := recordTurn(t, s, p, "v1", observation(2)); v.LastCheckedAt != observation(2).At {
		t.Fatalf("row after a same-outcome check = %+v", v)
	}
	if same, _ := os.ReadFile(path); !bytes.Equal(same, first) {
		t.Fatal("a check that changed only times rewrote the copy")
	}
	failed := observation(3)
	failed.Successful = false
	if v := recordTurn(t, s, p, "v1", failed); v.Status != Unhealthy {
		t.Fatalf("view after a failed turn = %+v", v)
	}
	var copied record
	if err := store.ReadJSON(path, &copied); err != nil || copied.Observed.Successful {
		t.Fatalf("copy after a failed turn = %+v, %v; want the failure", copied, err)
	}
	// The row is current even where the copy is not.
	if v := s.View(p, "v1"); v.Status != Unhealthy || v.LastCheckedAt != failed.At {
		t.Fatalf("view = %+v", v)
	}
}
