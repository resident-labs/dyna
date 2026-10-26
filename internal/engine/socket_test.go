package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/service"
	cliaccounts "dyna-agent/internal/cli/accounts"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store"
)

func TestUsageRefreshOverEngineSocket(t *testing.T) {
	services := testServices(t, false)
	calls, _, err := callsFor(services, false)
	if err != nil {
		t.Fatal(err)
	}
	path := enginetest.Serve(t, store.DataDir(), calls)
	client := engineclient.New(path)
	cmd := cliaccounts.Command(cliaccounts.Options{Client: client})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--refresh", "--json"})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), `"checkedAt"`) {
		t.Fatalf("accounts output: %s", &out)
	}
	var snapshot service.AccountsSnapshot
	if err := client.Call(ctx, "AccountsList", []any{"", false}, &snapshot); err != nil || snapshot.CheckedAt.IsZero() {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if err := client.Call(ctx, "NoSuchMethod", nil, nil); err == nil || !strings.Contains(err.Error(), "no method NoSuchMethod") {
		t.Fatalf("unknown method = %v", err)
	}
	var health Health
	if err := client.Call(ctx, "Health", nil, &health); err != nil || health.Status != "" || health.Degraded || health.Started.IsZero() {
		t.Fatalf("Health = %+v, %v", health, err)
	}
}

func TestSocketPermissionsAndLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dyna")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "engine.sock")
	listener, err := listenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	for path, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	if other, err := listenSocket(path); err == nil {
		other.Close()
		t.Fatal("replaced a live socket")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed socket still exists: %v", err)
	}
}

func TestSocketRejectsFilesAndSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "engine.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenSocket(path); err == nil {
		listener.Close()
		t.Fatal("replaced a regular file")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "keep" {
		t.Fatalf("file = %q, %v", body, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenSocket(filepath.Join(link, "engine.sock")); err == nil {
		listener.Close()
		t.Fatal("accepted symlink directory")
	}
}

// A scratch engine must not take the owner's live socket: when its data folder
// is its own but its runtime folder is the login one, it refuses to start.
func TestRefuseOwnerSocketForAForeignDataFolder(t *testing.T) {
	owner := store.OwnerDataDir()
	if owner == "" {
		t.Skip("this host has no owner home to compare against")
	}
	login := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	scratch := t.TempDir()
	err := checkPrivateRuntime(scratch, login)
	if err == nil {
		t.Fatal("a scratch data folder took the owner's runtime socket")
	}
	if !strings.Contains(err.Error(), "runtime folder of its own") {
		t.Fatalf("unhelpful refusal: %v", err)
	}
	// The owner's own data folder with the login runtime folder is the normal
	// service case, and a scratch engine with its own runtime folder is fine.
	for _, ok := range [][2]string{{owner, login}, {scratch, filepath.Join(scratch, "run")}, {scratch, ""}} {
		if err := checkPrivateRuntime(ok[0], ok[1]); err != nil {
			t.Fatalf("data=%q runtime=%q: %v", ok[0], ok[1], err)
		}
	}
}
