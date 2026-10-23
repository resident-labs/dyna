package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigrationPersistsReasonWithoutChangingWait(t *testing.T) {
	s, id := turnStore(t)
	raw := []byte(`{"n":1,"prompt":"retained prompt","future":{"keep":true},"usageNormalization":{"version":1},"deliveries":[{"ts":123,"kind":"steering","outcome":"unsupported","waitedMs":30000,"future":{"keep":true}},{"ts":124,"kind":"journal","outcome":"bound-hit","waitedMs":30000},{"ts":125,"kind":"steering","outcome":"interrupted","reason":"future-reason"}]}`)
	path := turnFile(t, s, id, 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	converted, revision, err := storedTurn(t.Context(), db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatal(err)
	}
	entries := want["deliveries"].([]any)
	for i, reason := range []string{"no-tool-boundaries", "wait-bound"} {
		d := entries[i].(map[string]any)
		d["outcome"], d["reason"] = "interrupted", reason
	}
	for _, field := range []string{"prompt", "future", "deliveries"} {
		if !reflect.DeepEqual(got[field], want[field]) {
			t.Fatalf("%s = %#v; want %#v", field, got[field], want[field])
		}
	}
	source, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, raw) {
		t.Fatalf("migration changed legacy source: %s, %v", source, err)
	}
	if err := database.Close(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	db, err = s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	replayed, after, err := storedTurn(t.Context(), db, id, 1)
	if err != nil || after != revision || !bytes.Equal(replayed, converted) {
		t.Fatalf("replay changed row: revision %d -> %d, %v", revision, after, err)
	}
}
