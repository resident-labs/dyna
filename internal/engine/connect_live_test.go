package engine

import (
	"errors"
	"strings"
	"testing"
)

// Only the live engine joins Resident Labs Connect. A read-only engine (QA),
// or a writable one outside its dyna-engine.service unit, must never report
// its ports to the relay, or the owner's remote access follows it.
func TestOnlyTheLiveEngineJoinsConnect(t *testing.T) {
	const (
		unit    = "/user.slice/user-1000.slice/user@1000.service/app.slice/dyna-engine.service"
		shell   = "/user.slice/user-1000.slice/user@1000.service/app.slice/app-term.scope"
		noUser  = "/docker/abc"
		owner   = "/home/o/.local/share/dyna"
		custom  = "/data/dyna"
		testDir = "/tmp/test/dyna"
	)
	for _, tc := range []struct {
		name     string
		readOnly bool
		group    string
		data     string
		want     string
	}{
		{"live engine in its unit", false, unit, owner, ""},
		{"live engine in its unit, custom XDG_DATA_HOME", false, unit, custom, ""},
		{"read-only QA engine", true, unit, owner, "read-only"},
		{"engine started from a shell under the user manager", false, shell, owner, "does not run in dyna-engine.service"},
		{"no user manager, owner's data folder", false, noUser, owner + "/", ""},
		{"no user manager, a test folder", false, noUser, testDir, "no user systemd manager"},
		{"cgroup unknown, a test folder", false, "", testDir, "no user systemd manager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := connectRefusal(tc.readOnly, tc.group, tc.data, owner)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("connectRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// A live engine whose migration failed runs read-only but keeps Connect, so
// the owner can see why from the phone. An engine started read-only (a
// second, QA engine) stays out.
func TestFailedMigrationKeepsConnect(t *testing.T) {
	const unit = "/user.slice/user-1000.slice/user@1000.service/app.slice/dyna-engine.service"
	dir := t.TempDir()
	saved := migrateStores
	t.Cleanup(func() { migrateStores = saved })
	keepSchemaOwner(t)
	migrateStores = func(string) error { return errors.New("boom") }
	readOnly, status, refused := startMode(dir, false, unit, dir)
	if !readOnly || status == "" || refused != "" {
		t.Fatalf("failed migration: readOnly=%v status=%q connect refused %q; want read-only with a status, still on Connect", readOnly, status, refused)
	}
	migrateStores = func(string) error { return nil }
	if readOnly, _, refused := startMode(dir, true, unit, dir); !readOnly || !strings.Contains(refused, "read-only") {
		t.Fatalf("--read-only engine: readOnly=%v connect refused %q; want read-only and off Connect", readOnly, refused)
	}
}
