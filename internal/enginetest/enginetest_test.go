package enginetest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workflowtemplate"
)

// A command's default client reaches the fake engine, which answers through
// the same call handling as the real socket, with no running service.
func TestDefaultClientReachesTheFakeEngine(t *testing.T) {
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "Echo", Access: websvc.Read, Scope: websvc.ScopeRead, Method: func(text string) string { return "echo: " + text }},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := enginetest.Serve(t, t.TempDir(), calls)
	if !engineclient.Running("") {
		t.Fatalf("no engine answers at %s", path)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var reply string
	if err := engineclient.New("").Call(ctx, "Echo", []any{"hi"}, &reply); err != nil || reply != "echo: hi" {
		t.Fatalf("Echo = %q, %v", reply, err)
	}
	if err := engineclient.New("").Call(ctx, "Missing", nil, nil); err == nil || !strings.Contains(err.Error(), "no method Missing") {
		t.Fatalf("unknown method = %v", err)
	}
}

// A fake engine must create the same store tables before it serves calls.
func TestFakeEngineMigratesBeforeServing(t *testing.T) {
	dataDir := t.TempDir()
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "StoresReady", Access: websvc.Read, Scope: websvc.ScopeRead, Method: func() (bool, error) {
			db, err := database.Open(dataDir)
			if err != nil {
				return false, err
			}
			var count int
			err = db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('state_mirrors','hook_runtime')").Scan(&count)
			return count == 2, err
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	servePath := enginetest.Serve(t, dataDir, calls)
	t.Cleanup(func() { database.Close(dataDir) })
	var ready bool
	if err := engineclient.New(servePath).Call(t.Context(), "StoresReady", nil, &ready); err != nil || !ready {
		t.Fatalf("fake engine served before stores migrated: ready=%v, error=%v", ready, err)
	}
}

func TestFakeEngineMigratesOnlyItsExplicitDataFolder(t *testing.T) {
	other := t.TempDir()
	t.Setenv("XDG_DATA_HOME", other)
	dataDir := t.TempDir()
	enginetest.Serve(t, dataDir, map[string]websvc.Call{})
	if _, err := os.Stat(filepath.Join(dataDir, "dyna.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "dyna", "dyna.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake engine migrated the caller's unrelated data folder: %v", err)
	}
}

// fatalRecorder stops a fake engine's start at its first Fatal and keeps
// the message, so a test can check a refusal.
type fatalRecorder struct {
	testing.TB
	msg string
}

type stopped struct{}

func (r *fatalRecorder) Fatal(args ...any) {
	r.msg = strings.TrimSpace(fmt.Sprintln(args...))
	panic(stopped{})
}
func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	panic(stopped{})
}

// The fake engine refuses a data folder outside the temporary folder, such
// as the CI user's own dyna.db, before it migrates anything there.
func TestFakeEngineRefusesARealDataFolder(t *testing.T) {
	// A folder beside the temporary folder stands in for a real data folder.
	// It is never created: the refusal comes first.
	real := filepath.Join(filepath.Dir(os.TempDir()), fmt.Sprintf("dyna-enginetest-%d", os.Getpid()))
	r := &fatalRecorder{TB: t}
	func() {
		defer func() {
			if v := recover(); v != nil && v != (stopped{}) {
				panic(v)
			}
		}()
		enginetest.Serve(r, real, nil)
	}()
	if !strings.Contains(r.msg, "outside the temporary folder") {
		t.Fatalf("Serve on %s = %q; want a refusal", real, r.msg)
	}
	if _, err := os.Stat(real); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused folder was created: %v", err)
	}
}

// Each fake engine must leave the caller's template registry untouched.
func TestFakeEngineIsolatesTemplateRegistry(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	registry := workflowtemplate.DefaultRegistryDir()
	dataDir := t.TempDir()
	enginetest.Serve(t, dataDir, map[string]websvc.Call{})
	if got := workflowtemplate.DefaultRegistryDir(); got != registry {
		t.Fatalf("fake engine hid caller config: registry = %s; want %s", got, registry)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var migratedRegistry string
	if err := db.QueryRow("SELECT registry FROM workflow_template_seeds").Scan(&migratedRegistry); err != nil {
		t.Fatal(err)
	}
	if migratedRegistry == registry {
		t.Fatalf("fake engine migrated caller template registry %s", registry)
	}
	if _, err := os.Stat(registry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake engine touched caller template registry: %v", err)
	}
}
