package ci

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
)

// Exercise the stored rule through both placement and lease renewal, not just admission in isolation.
func TestRepositoryHostsOverrideRoomForPlacementAndLease(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := newFixture(1)
	defer f.stop()
	if err := s.CreateRoom(ctx, "room", "room", f.now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", repo, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomOnly(ctx, "room", []string{"agents"}); err != nil {
		t.Fatal(err)
	}
	load := func(hosts []string) {
		t.Helper()
		if err := s.SetRepoCI(ctx, repo, true, 1, hosts); err != nil {
			t.Fatal(err)
		}
		rows, err := s.CIRepos(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.store.repos = rows
	}
	load([]string{"pve"})
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatal("repository host did not renew the lease")
	}
	f.scale(1)
	if len(f.drivers["pve"].runners) != 1 {
		t.Fatal("room rule prevented repository placement")
	}
	// A changed override excludes the only host, even though it is healthy and has ci on.
	load([]string{"other"})
	f.now = f.now.Add(leaseEvery)
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatal("excluded host renewed the lease")
	}
	// Clearing uses the room rule, which still excludes pve.
	load([]string{})
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatal("empty override did not fall back to room rule")
	}
	f.addHost(readyHost("agents", 64*box.GiB))
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatal("room host did not renew after clearing override")
	}
	// Remove the finished runner so the next job must be placed using the fallback.
	f.drivers["pve"].runners = nil
	f.scale(1)
	if len(f.drivers["agents"].runners) != 1 {
		t.Fatal("cleared override did not place on room host")
	}
}
