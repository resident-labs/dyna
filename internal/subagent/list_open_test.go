package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A listing of open sessions never reads closed records. On the owner's
// machine almost all 4,600 sessions are closed, and reading each one on every
// feed and room listing was a large share of the engine's CPU.
func TestOpenListingSkipsClosedRecords(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "sessions")}
	open := "ses_20261020-100000-aaaaaaaa"
	closed := "ses_20261020-100000-bbbbbbbb"
	for _, m := range []Meta{
		{ID: open, Name: "open", Room: "work", RoomID: "r", Depth: 1},
		{ID: closed, Name: "closed", Room: "work", RoomID: "r", Depth: 1, Closed: time.Now()},
	} {
		if err := os.MkdirAll(filepath.Join(s.Root, m.ID), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := s.writeMeta(m); err != nil {
			t.Fatal(err)
		}
	}
	infoCache.Delete(s.recordKey(closed))
	infos, err := s.ListSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != open {
		t.Fatalf("open listing = %+v, want only %s", infos, open)
	}
	if _, read := infoCache.Load(s.recordKey(closed)); read {
		t.Fatal("open listing read the closed session's record")
	}
	all, err := s.ListSnapshot(true)
	if err != nil || len(all) != 2 {
		t.Fatalf("full listing = %d sessions, %v; want both", len(all), err)
	}
}
