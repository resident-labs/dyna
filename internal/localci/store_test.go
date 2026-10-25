package localci

import (
	"context"
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
	if err != nil || !reflect.DeepEqual(nodes, []Node{{Name: "hermes", SSH: "pip@hermeslab", Slots: 2, Enabled: true}}) {
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
		{Name: "node", Slots: 0},
		{Name: "node", Slots: 1, SSH: "-oProxyCommand=exec"},
		{Name: "node", Slots: 1, SSH: "user@host; touch /tmp/injected"},
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
