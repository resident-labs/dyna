package podman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// On real rootless Podman: two runners of different sizes on one host each write their own build folder inside the host's
// cache, read the rest of it, and change nothing else there, and a folder past the cap is emptied before the next job.
// DYNA_PODMAN_TEST_CI_BASE names a local image with sh, sleep and touch. Run it in a store of its own, with
// CONTAINERS_STORAGE_CONF, since it makes the proxy's volume under its real name.
func TestRealRunnersKeepTheirOwnBuildFolders(t *testing.T) {
	base := os.Getenv("DYNA_PODMAN_TEST_CI_BASE")
	if base == "" {
		t.Skip("DYNA_PODMAN_TEST_CI_BASE names a local image; this test runs real podman")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := New("local", t.TempDir(), "host", hereShell{})
	if err != nil {
		t.Fatal(err)
	}
	// The job's files belong to a subordinate UID, so only Podman's user namespace can remove them.
	dir, err := os.MkdirTemp("", "dyna-ci-cache-")
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Command("podman", "unshare", "rm", "-rf", dir).Run()
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "gomod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "gomod", "seed"), []byte("from a box\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sockets := filepath.Join(dir, "sock")
	if err := os.Mkdir(sockets, 0o755); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	image := fmt.Sprintf("localhost/dyna-ci-realtest:%d", stamp)
	file := fmt.Sprintf("FROM %s\nENTRYPOINT [\"sleep\", \"600\"]\n", base)
	build := exec.CommandContext(ctx, "podman", "build", "--quiet", "--tag", image, "-f", "-", dir)
	build.Stdin = strings.NewReader(file)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	defer exec.Command("podman", "rmi", "--force", image).Run()
	// The proxy makes a folder per slot in its volume. This test needs only the folders, not the proxy.
	realPodman(t, "volume", "create", egressName)
	defer exec.Command("podman", "volume", "rm", "--force", egressName).Run()
	volume := realPodman(t, "volume", "inspect", "--format", "{{.Mountpoint}}", egressName)
	realPodman(t, "unshare", "mkdir", "-p", filepath.Join(volume, "0"), filepath.Join(volume, "1"))

	small, _ := box.Size(contract.BoxSizeSmall)
	large, _ := box.Size(contract.BoxSizeLarge)
	specs := []box.RunnerSpec{
		{Name: fmt.Sprintf("dyna-ci-realtest-%d-small", stamp), Image: image, Config: "jit", Repo: "owner/repo", ID: 1, Slot: 0, Size: small, Cache: cache, BuildCache: sockets},
		{Name: fmt.Sprintf("dyna-ci-realtest-%d-large", stamp), Image: image, Config: "jit", Repo: "owner/repo", ID: 2, Slot: 1, Size: large, Cache: cache, BuildCache: sockets},
	}
	for _, spec := range specs {
		defer exec.Command("podman", "--cgroup-manager=systemd", "rm", "--force", "--time", "0", spec.Name).Run()
		if err := h.StartRunner(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	found, _, err := h.ListRunners(ctx)
	if err != nil || len(found) != 2 || found[0].Size.Name == found[1].Size.Name {
		t.Fatalf("listed %+v: %v", found, err)
	}
	for i, spec := range specs {
		other := buildFolder(1 - spec.Slot)
		script := fmt.Sprintf(`set -e
cat /cache/gomod/seed
echo %s > "$DYNA_CI_BUILD/proof"
touch "$CARGO_HOME/proof" "$CARGO_TARGET_DIR/proof"
if touch /cache/escape 2>/dev/null; then echo wrote the cache; exit 1; fi
if touch /cache/gomod/seed 2>/dev/null; then echo wrote a box file; exit 1; fi
if touch /cache/%s/intrude 2>/dev/null; then echo wrote another slot; exit 1; fi
echo "$DYNA_CI_CACHE $DYNA_CI_BUILD"`, spec.Size.Name, other)
		out := realPodman(t, "exec", spec.Name, "sh", "-c", script)
		if want := "from a box\n/cache /build"; out != want {
			t.Fatalf("runner %d said %q, want %q", i, out, want)
		}
		written, err := os.ReadFile(filepath.Join(cache, buildFolder(spec.Slot), "proof"))
		if err != nil || strings.TrimSpace(string(written)) != string(spec.Size.Name) {
			t.Fatalf("slot %d folder on the host holds %q: %v", spec.Slot, written, err)
		}
		for _, folder := range []string{"cargo", "target"} {
			if _, err := os.Stat(filepath.Join(cache, buildFolder(spec.Slot), folder, "proof")); err != nil {
				t.Fatalf("slot %d's %s folder is not Cargo's: %v", spec.Slot, folder, err)
			}
		}
		t.Logf("%s runner: wrote %s on the host, and nothing else", spec.Size.Name, filepath.Join(cache, buildFolder(spec.Slot), "proof"))
	}
	if _, err := os.Stat(filepath.Join(cache, "escape")); err == nil {
		t.Fatal("a runner wrote the cache outside its folder")
	}

	// A folder past the cap is emptied, and the next runner gets it back empty and its own.
	realPodman(t, "--cgroup-manager=systemd", "rm", "--force", "--time", "0", specs[0].Name)
	if _, err := h.run(ctx, "bash", "-c", buildScript, "dyna-ci-build", filepath.Join(cache, buildFolder(0)), "0"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(cache, buildFolder(0)))
	if err != nil || len(entries) != 2 || entries[0].Name() != "cargo" || entries[1].Name() != "target" {
		t.Fatalf("a folder past the cap kept %v: %v", entries, err)
	}
	for _, entry := range entries {
		if inside, err := os.ReadDir(filepath.Join(cache, buildFolder(0), entry.Name())); err != nil || len(inside) != 0 {
			t.Fatalf("a folder past the cap kept %s/%v: %v", entry.Name(), inside, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, buildFolder(1), "proof")); err != nil {
		t.Fatalf("emptying one slot touched another: %v", err)
	}
}
