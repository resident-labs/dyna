package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

// The cache starts once, and its socket's folder is there and open to a runner's user whether or not it starts.
func TestBuildCacheStartsOnlyWhenItIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	state := filepath.Join(dir, "up")
	layout := box.Layout{HostData: filepath.Join(dir, "host")}
	h, err := New("user@host", t.TempDir(), "host", localShell{path: fakeTools(t, map[string]string{
		"systemctl":   "[ -e " + state + " ]",
		"systemd-run": `echo "$*" >> ` + log + "\ntouch " + state,
	}) + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.EnsureBuildCache(context.Background(), layout); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "build-cache serve --dir " + layout.HostBuildCache() + " --listen " + layout.HostBuildCacheSocket()
	if got := strings.Count(string(calls), want); got != 1 {
		t.Fatalf("started %d times: %s", got, calls)
	}
	info, err := os.Stat(layout.HostBuildCacheSocketFolder())
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("socket folder %v: %v", info, err)
	}
}
