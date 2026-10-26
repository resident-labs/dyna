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
	"dyna-agent/internal/mode"
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

// An engine a Dyna session started must not take the owner's live socket. The
// installed service and an engine the owner starts from a terminal keep the
// normal runtime folder, including with a custom XDG_DATA_HOME.
func TestRefuseOwnerSocketForASessionLaunchedEngine(t *testing.T) {
	login := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	err := checkPrivateRuntime(true, login)
	if err == nil {
		t.Fatal("a session-launched engine took the owner's runtime socket")
	}
	if !strings.Contains(err.Error(), "runtime folder of its own") {
		t.Fatalf("unhelpful refusal: %v", err)
	}
	scratch := t.TempDir()
	for _, ok := range []struct {
		stamped bool
		runtime string
	}{
		{true, filepath.Join(scratch, "run")},
		{false, login},
		{false, filepath.Join(scratch, "run")},
		{true, ""},
	} {
		if err := checkPrivateRuntime(ok.stamped, ok.runtime); err != nil {
			t.Fatalf("stamped=%t runtime=%q: %v", ok.stamped, ok.runtime, err)
		}
	}
}

// An engine refuses a socket that belongs to an engine on another data folder,
// so a second engine cannot serve the owner's socket.
func TestEngineRefusesAnotherDataFoldersSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.sock")
	owner, other := filepath.Join(dir, "owner-data"), filepath.Join(dir, "other-data")
	if err := writeSocketOwner(path, owner); err != nil {
		t.Fatal(err)
	}
	err := checkSocketOwner(path, other)
	if err == nil {
		t.Fatal("an engine accepted a socket that belongs to another data folder")
	}
	if !strings.Contains(err.Error(), owner) {
		t.Fatalf("the refusal does not name the owning data folder: %v", err)
	}
	if err := checkSocketOwner(path, owner); err != nil {
		t.Fatalf("the owning data folder was refused its own socket: %v", err)
	}
	// A socket with no owner record is not refused; the running check decides.
	if err := checkSocketOwner(filepath.Join(dir, "fresh.sock"), other); err != nil {
		t.Fatalf("a socket without an owner record was refused: %v", err)
	}
}

// The launch stamp is read before the engine clears its environment: a session
// launched engine must still refuse the login socket, even though nothing is
// recorded beside the socket yet and the owner's listener is down.
func TestLaunchStampSurvivesTheEnvironmentReset(t *testing.T) {
	t.Setenv(mode.EnvRoomID, "room_test")
	t.Setenv(mode.EnvRoom, "test")
	launchedBySession.Store(false)
	dropDynaEnv()
	if _, stamped := mode.StampFromEnv(); stamped {
		t.Skip("this build keeps the stamp in the environment, so the ordering is trivially safe")
	}
	if !launchedBySession.Load() {
		t.Fatal("clearing the environment lost the launch stamp")
	}
	// The recorded value is what the bind check uses, and it refuses a launched
	// engine on the login runtime folder with no owner record present.
	login := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	if err := checkPrivateRuntime(launchedBySession.Load(), login); err == nil {
		t.Fatal("a launched engine took the login socket when no owner record existed")
	}
}
