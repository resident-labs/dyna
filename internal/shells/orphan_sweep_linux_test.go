//go:build linux

package shells

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepOrphanDirectories(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	if _, err := s.db(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	orphan := "sh_20261020-120000-00000001"
	kept := "sh_20261020-120000-00000002"
	uncertain := "sh_20261020-120000-00000003"
	recent := "sh_20261020-120000-00000004"
	for _, id := range []string{orphan, kept, uncertain, recent, "events"} {
		dir := filepath.Join(s.Root, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if id != uncertain && id != "events" {
			if err := os.WriteFile(filepath.Join(dir, exitedMarker), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if id != recent {
				if err := os.Chtimes(filepath.Join(dir, exitedMarker), old, old); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(Meta{ID: kept, Cmd: []string{"true"}, Cwd: "/", Started: old, Keep: true}, &Exit{Ended: old}); err != nil {
		t.Fatal(err)
	}
	n, err := s.SweepOrphanDirectories(context.Background(), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want one", n, err)
	}
	for _, id := range []string{orphan, kept, uncertain, recent, "events"} {
		_, err := os.Stat(filepath.Join(s.Root, id))
		if (id == orphan && !os.IsNotExist(err)) || (id != orphan && err != nil) {
			t.Fatalf("directory %s: %v", id, err)
		}
	}
	// EOF resets the cursor; a later scan revisits a previously young marker.
	if _, err := s.SweepOrphanDirectories(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SweepOrphanDirectories(context.Background(), time.Now().Add(48*time.Hour)); err != nil || n != 1 {
		t.Fatalf("later sweep removed %d, %v; want the aged directory", n, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, recent)); !os.IsNotExist(err) {
		t.Fatalf("aged directory remains: %v", err)
	}
}
