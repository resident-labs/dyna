package webpush

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestRemovedSubscriptionIsNotReimported(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	old := Entry{DeviceID: "phone", Endpoint: "https://example.com/push", P256dh: "key", Auth: "auth", Created: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	writeOldSubscriptions(t, dir, []Entry{old})
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe("phone"); err != nil {
		t.Fatal(err)
	}
	old.Endpoint = "https://example.com/stale"
	writeOldSubscriptions(t, dir, []Entry{old})
	path := filepath.Join(dir, "connect", "push", subsFile)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List()
	if err != nil || len(got) != 0 {
		t.Fatalf("removed subscription returned: %+v, %v", got, err)
	}
	if err := s.Unsubscribe("unrelated"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, after) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}
