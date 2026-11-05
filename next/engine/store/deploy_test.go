package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/box"
	"testing"
)

func TestDeployHostsReadsOldSchemaWithoutOpeningStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, h := range []box.Host{{Name: "ready", SSH: "user@ready", State: box.HostReady}, {Name: "offline", SSH: "user@offline", State: box.HostUnreachable}, {Name: "exec", SSH: "user@exec", State: box.HostReady, ExecOnly: true}} {
		if err := s.AddHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	// Pretend an older deployed engine owns this schema version. A Store.Open would fail its lock or try a migration.
	if err := s.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec("PRAGMA user_version=1"); return err }); err != nil {
		t.Fatal(err)
	}
	hosts, err := DeployHosts(ctx, dir)
	if err != nil || len(hosts) != 1 || hosts[0].SSH != "user@ready" {
		t.Fatalf("snapshot: %+v %v", hosts, err)
	}
	var version int
	if err := s.Read().QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema changed: %d %v", version, err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "later", SSH: "user@later", State: box.HostReady}); err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatal("snapshot changed when a host was added")
	}
}
