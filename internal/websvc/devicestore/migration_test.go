package devicestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestLegacyDevicesAndPairingImportOnlyOnce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "web")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	code := "ABCDEFGHJK"
	mac, err := (&Store{dir: dir}).codeMAC(code)
	if err != nil {
		t.Fatal(err)
	}
	device := Device{ID: "legacy", Name: "laptop", Hash: sum("legacy-token"), Created: now, LastSeen: now, Expires: now.Add(DeviceLife)}
	seed := func() {
		t.Helper()
		for name, value := range map[string]any{"devices.json": []Device{device}, "pair.json": pairing{ID: "paired", Hash: mac, Expires: now.Add(CodeLife), Tries: CodeTries - 2, Scopes: []string{"read"}}} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	s.SetClock(func() time.Time { return now })
	if _, ok := s.Check("legacy-token"); !ok {
		t.Fatal("imported device was rejected")
	}
	if _, _, err := s.Pair("AAAAAAAAAA", "wrong"); !errors.Is(err, ErrPair) {
		t.Fatalf("wrong code: %v", err)
	}
	_, paired, err := s.Pair(code, "phone")
	if err != nil || paired.ID != "paired" || len(paired.Scopes) != 1 || paired.Scopes[0] != "read" {
		t.Fatalf("imported pairing: %+v %v", paired, err)
	}
	if _, err := s.Revoke("legacy"); err != nil {
		t.Fatal(err)
	}
	seed() // Retained JSON must not resurrect the revoked device or consumed code.
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.SetClock(func() time.Time { return now })
	if _, ok := reopened.Check("legacy-token"); ok {
		t.Fatal("retained JSON restored a revoked device")
	}
	if _, _, err := reopened.Pair(code, "again"); !errors.Is(err, ErrPair) {
		t.Fatalf("retained JSON restored a consumed code: %v", err)
	}
}
