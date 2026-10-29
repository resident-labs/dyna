package localci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/state"
)

func fixtureStore(t *testing.T) Store {
	t.Helper()
	s := Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	return s
}

// TestMigrationFromVersion5CreatesCacheCleanGeneration upgrades an existing
// store, not just a fresh temporary one. The old table remains, and the new
// generation starts at zero and advances after the reopen.
func TestMigrationFromVersion5CreatesCacheCleanGeneration(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(db, "localci.schema", 5, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), cacheCleanSchema)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	generation, err := s.CacheCleanGeneration(t.Context(), "node-a")
	if err != nil || generation != 0 {
		t.Fatalf("upgraded generation = %d, %v; want zero", generation, err)
	}
	if err := s.AdvanceCacheCleanGeneration(t.Context(), "node-a"); err != nil {
		t.Fatalf("advance upgraded generation: %v", err)
	}
	generation, err = s.CacheCleanGeneration(t.Context(), "node-a")
	if err != nil || generation != 1 {
		t.Fatalf("advanced generation = %d, %v; want one", generation, err)
	}
	db, err = s.open()
	if err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='localci.schema'").Scan(&version); err != nil || version != "6" {
		t.Fatalf("schema version = %q, %v; want 6", version, err)
	}
}

func TestStoreRoomModes(t *testing.T) {
	ctx := context.Background()
	s := fixtureStore(t)
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var events []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) { events = append(events, ev) })
	if mode, err := s.Mode(ctx, "room-a"); err != nil || mode != ModeGitHub {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
	if err := s.SetMode(ctx, "room-a", ModeLocal); err != nil {
		t.Fatal(err)
	}
	if mode, err := s.Mode(ctx, "room-b"); err != nil || mode != ModeGitHub {
		t.Fatalf("another room inherited mode = %q, %v", mode, err)
	}
	if len(events) != 1 || events[0].Topic != Topic || events[0].Room != "room-a" {
		t.Fatalf("committed events = %#v", events)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if mode, err := s.Mode(ctx, "room-a"); err != nil || mode != ModeLocal {
		t.Fatalf("persisted mode = %q, %v", mode, err)
	}
}

// Nodes reads the machines with the ci role, and the local machine runs on
// this host, so it carries no SSH destination.
func TestStoreNodesReadsCIMachines(t *testing.T) {
	ctx := context.Background()
	s := fixtureStore(t)
	registry := machines.Store{DataDir: s.DataDir}
	if _, err := registry.Save(ctx, machines.Machine{Name: "hermes", Roles: []machines.Role{machines.RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	other, err := registry.Save(ctx, machines.Machine{Name: "build-only", Roles: []machines.Role{machines.RoleAgents}, CISlots: 0, Enabled: true, SSH: "ari@build"})
	if err != nil {
		t.Fatal(err)
	}
	if other.HasRole(machines.RoleCI) {
		t.Fatalf("agents machine gained the ci role: %#v", other)
	}
	nodes, err := s.Nodes(ctx)
	if err != nil || !reflect.DeepEqual(nodes, []Node{{Name: "hermes", SSH: "pip@hermeslab", Slots: 2, Enabled: true, ContainerSocket: machines.DefaultContainerSocket}}) {
		t.Fatalf("nodes = %#v, %v", nodes, err)
	}
	local, err := registry.GetLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	local.Roles = append(local.Roles, machines.RoleCI)
	local.CISlots = 4
	if _, err := registry.Save(ctx, local); err != nil {
		t.Fatal(err)
	}
	nodes, err = s.Nodes(ctx)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("nodes with local CI = %#v, %v", nodes, err)
	}
	if got := nodes[0]; got.SSH != "" || got.Slots != 4 || !got.Enabled {
		t.Fatalf("local CI node = %#v", got)
	}
}

func TestStoreRejectsUnsafeNodeConfiguration(t *testing.T) {
	for _, n := range []Node{
		{Name: "-option", Slots: 1},
		{Name: ""},
		{Name: "node", SSH: "-oProxyCommand=exec"},
		{Name: "node", SSH: "user@host; touch /tmp/injected"},
	} {
		if err := n.Validate(); err == nil {
			t.Errorf("accepted unsafe node %#v", n)
		}
	}
	s := fixtureStore(t)
	ctx := context.Background()
	if err := s.SetMode(ctx, "room", "LOCAL"); err == nil {
		t.Fatal("accepted invalid mode")
	}
	if err := s.SetMode(ctx, " ", ModeLocal); err == nil {
		t.Fatal("accepted empty room")
	}
	if err := s.SaveResult(ctx, Result{Repo: "/repo", SHA: "../../escape"}); err == nil {
		t.Fatal("accepted path traversal as a SHA")
	}
}

func TestStoreImportsResultOnceAndUsesCanonicalPath(t *testing.T) {
	ctx := context.Background()
	s := fixtureStore(t)
	repo := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	result := Result{Repo: repo, SHA: sha, Status: "passed", Suites: []SuiteResult{{Suite: "go", Status: "passed"}}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(s.DataDir, "local-ci", repoKey(repo), sha, "result.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadResult(ctx, alias, sha)
	if err != nil || !reflect.DeepEqual(loaded, result) {
		t.Fatalf("imported result = %#v, %v", loaded, err)
	}
	if err := os.WriteFile(legacy, []byte("invalid old data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadResult(ctx, repo, sha)
	if err != nil || loaded.Status != "passed" {
		t.Fatalf("result read stale legacy data = %#v, %v", loaded, err)
	}
	result.Repo = alias
	result.Status = "failed"
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var event hub.Event
	state.For(db).SetPublish(func(ev hub.Event) { event = ev })
	if err := s.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	if event.Topic != Topic || event.Key != "result:"+sha {
		t.Fatalf("result event = %#v", event)
	}
	loaded, err = s.LoadResult(ctx, repo, sha)
	if err != nil || loaded.Repo != repo || loaded.Status != "failed" {
		t.Fatalf("canonical saved result = %#v, %v", loaded, err)
	}
	if _, err := s.LoadResult(ctx, repo, strings.Repeat("b", 40)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing result = %v", err)
	}
}

func TestStoreHostedAttemptsPersist(t *testing.T) {
	s := fixtureStore(t)
	ctx := context.Background()
	repo := t.TempDir()
	sha := strings.Repeat("a", 40)
	if _, err := s.LoadHostedRuns(ctx, repo, sha); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing hosted attempts = %v", err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var event hub.Event
	state.For(db).SetPublish(func(ev hub.Event) { event = ev })
	runs := HostedRuns{IDs: []int64{11, 12}, Attempts: map[int64]int{11: 2, 12: 1}}
	if err := s.SaveHostedRuns(ctx, repo, sha, runs); err != nil {
		t.Fatal(err)
	}
	if event.Topic != Topic || event.Key != "hosted:"+sha {
		t.Fatalf("hosted event = %#v", event)
	}
	runs.Resumed = true
	if err := s.SaveHostedRuns(ctx, repo, sha, runs); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadHostedRuns(ctx, repo, sha)
	if err != nil || !reflect.DeepEqual(loaded, runs) {
		t.Fatalf("persisted hosted attempts = %#v, %v", loaded, err)
	}
	if _, err := s.LoadHostedRuns(ctx, repo, strings.Repeat("b", 40)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("another head inherited hosted attempts = %v", err)
	}
}

// A machine that names no suite takes every suite, and a run whose suites no
// one machine allows reports the suite that stops it, so a trial never waits
// for a machine that cannot take it.
func TestBlockedSuiteNamesTheSuiteNoMachineAllows(t *testing.T) {
	everySuite := []Node{{Name: "all", Enabled: true}}
	split := []Node{{Name: "go-only", Enabled: true, Suites: []string{"go"}}, {Name: "web-only", Enabled: true, Suites: []string{"frontend"}}}
	for _, tc := range []struct {
		name   string
		nodes  []Node
		suites []Suite
		want   Suite
		all    bool
	}{
		{name: "no suite named", nodes: everySuite, suites: []Suite{SuiteGo, SuiteFrontend}, all: true},
		{name: "one machine allows all", nodes: split[:1], suites: []Suite{SuiteGo}, all: true},
		{name: "each allowed elsewhere", nodes: split, suites: []Suite{SuiteGo, SuiteFrontend}},
		{name: "no machine allows it", nodes: split, suites: []Suite{SuiteGo, SuiteComputerSmoke}, want: SuiteComputerSmoke},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := BlockedSuite(tc.nodes, tc.suites)
			if tc.all {
				if err != nil {
					t.Fatalf("blocked a run the fleet can take: %v", err)
				}
				return
			}
			var blocked SuiteBlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("blocked run = %v, want a typed SuiteBlockedError", err)
			}
			if blocked.Suite != tc.want {
				t.Fatalf("blocked suite = %q, want %q", blocked.Suite, tc.want)
			}
			if tc.want != "" && !strings.Contains(err.Error(), string(tc.want)) {
				t.Fatalf("error does not name the suite: %v", err)
			}
		})
	}
}
