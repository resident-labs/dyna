package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestBrowserProfilesAndRooms(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	roomA := contract.RoomID("room_a")
	roomB := contract.RoomID("room_b")
	if err := s.CreateRoom(ctx, roomA, "alpha", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, roomB, "beta", now); err != nil {
		t.Fatal(err)
	}

	if err := s.BrowserCreate(ctx, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserCreate(ctx, "personal"); !errors.Is(err, ErrBrowserExists) {
		t.Fatalf("duplicate create = %v, want ErrBrowserExists", err)
	}

	// Room without profile returns empty.
	if prof, others, err := s.RoomBrowser(ctx, roomA); err != nil || prof != "" || len(others) != 0 {
		t.Fatalf("no-profile RoomBrowser: prof=%q others=%v err=%v", prof, others, err)
	}

	// Assign to roomA, then roomB.
	if err := s.SetRoomBrowser(ctx, roomA, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err := s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || len(others) != 0 {
		t.Fatalf("RoomBrowser alone: prof=%q others=%v err=%v", prof, others, err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, "personal"); err != nil {
		t.Fatal(err)
	}
	prof, others, err = s.RoomBrowser(ctx, roomA)
	if err != nil || prof != "personal" || !slices.Equal(others, []string{"beta"}) {
		t.Fatalf("RoomBrowser shared: prof=%q others=%v err=%v", prof, others, err)
	}

	// BrowserList lists rooms.
	list, err := s.BrowserList(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "personal" || !slices.Equal(list[0].Rooms, []string{"alpha", "beta"}) {
		t.Fatalf("BrowserList: %+v %v", list, err)
	}

	// Remove refused while rooms reference it.
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrBrowserInUse) {
		t.Fatalf("remove while referenced = %v, want ErrBrowserInUse", err)
	}

	// Unassign rooms and remove profile.
	if err := s.SetRoomBrowser(ctx, roomA, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomBrowser(ctx, roomB, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BrowserRemove(ctx, "personal"); err != nil {
		t.Fatalf("BrowserRemove: %v", err)
	}
	if err := s.BrowserRemove(ctx, "personal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove = %v, want ErrNotFound", err)
	}
}

func TestBrowserLeasesMigrationDropsTableAndKeepsProfiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Downgrade to schema 127 (v1 browsers + browser_leases with active lease).
	_, err = s.writer.Exec(`
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes,browser) VALUES('room_life','life',0,0,0,'personal');
		ALTER TABLE browsers ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
		INSERT INTO browsers(name,generation) VALUES('personal',4);
		CREATE TABLE browser_leases (profile TEXT PRIMARY KEY REFERENCES browsers(name) ON DELETE CASCADE, box TEXT NOT NULL, session TEXT NOT NULL, generation INTEGER NOT NULL, since INTEGER NOT NULL);
		INSERT INTO browser_leases(profile,box,session,generation,since) VALUES('personal','box_1','ses_1',4,1700000000000);
		UPDATE meta SET value='127' WHERE key='schema_version';
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("upgrade 127->128: %v", err)
	}
	defer s.Close()

	// browser_leases must be gone.
	var count int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='browser_leases'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("browser_leases still exists: count=%d err=%v", count, err)
	}
	// browsers.generation must be gone, while personal and room assignment survive.
	if _, err := s.Read().Exec(`SELECT generation FROM browsers`); err == nil {
		t.Fatal("browsers.generation column still exists")
	}
	prof, _, err := s.RoomBrowser(context.Background(), "room_life")
	if err != nil || prof != "personal" {
		t.Fatalf("RoomBrowser after migration: prof=%q err=%v", prof, err)
	}
	list, err := s.BrowserList(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "personal" || !slices.Equal(list[0].Rooms, []string{"life"}) {
		t.Fatalf("BrowserList after migration: %+v %v", list, err)
	}
}
