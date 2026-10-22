//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
)

func TestResourceRunnerCrashHelper(t *testing.T) {
	if os.Getenv("RUNTIME_RESOURCE_CRASH_HELPER") != "1" {
		return
	}
	account := &Accounts{Root: os.Getenv("RESOURCE_ACCOUNT_ROOT")}
	bundles := &BundleManager{Root: os.Getenv("RESOURCE_BUNDLE_ROOT")}
	var bundle Bundle
	if err := json.Unmarshal([]byte(os.Getenv("RESOURCE_BUNDLE")), &bundle); err != nil {
		t.Fatal(err)
	}
	turn, err := account.TurnLeaseManaged(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	binary, err := bundles.LeaseManaged(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	controller, err := processguard.New(context.Background(), "/bin/sh", "-c", `sleep 300 & echo "$!" > "$RESOURCE_CHILD_PID"; wait`)
	if err != nil {
		t.Fatal(err)
	}
	controller.Cmd().Env = append(os.Environ(), "RESOURCE_CHILD_PID="+os.Getenv("RESOURCE_READY"))
	if err = controller.Start(func(ref processguard.Reference) error {
		raw, e := json.Marshal(ref)
		if e != nil {
			return e
		}
		if e = turn.Bind(raw); e != nil {
			return e
		}
		return binary.Bind(raw)
	}); err != nil {
		t.Fatal(err)
	}
	if err = controller.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCrashDrainsGrandchildBeforeResourcesBecomeUnused(t *testing.T) {
	accounts := testAccounts(t)
	bundles := manager(t)
	old := install(t, bundles, "codex", "1.0.0")
	_ = install(t, bundles, "codex", "2.0.0")
	_ = install(t, bundles, "codex", "3.0.0")
	raw, _ := json.Marshal(old)
	ready := filepath.Join(t.TempDir(), "grandchild.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestResourceRunnerCrashHelper$")
	cmd.Env = append(os.Environ(), "RUNTIME_RESOURCE_CRASH_HELPER=1", "RESOURCE_ACCOUNT_ROOT="+accounts.Root, "RESOURCE_BUNDLE_ROOT="+bundles.Root, "RESOURCE_BUNDLE="+string(raw), "RESOURCE_READY="+ready)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if info, e := os.Stat(ready); e == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native grandchild did not start", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	paths, err := resourceLeasePaths(filepath.Join(accounts.Root, "codex", ".turns"), "")
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	live, use, err := resourceLeaseState(paths[0])
	if err != nil || !live {
		t.Fatal(live, err)
	}
	var ref processguard.Reference
	if err = json.Unmarshal(use.Reference, &ref); err != nil {
		t.Fatal(err)
	}
	defer processguard.Signal(ref, syscall.SIGKILL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if release, e := accounts.switchGate(ctx, "codex"); e == nil {
		release()
		cancel()
		t.Fatal("live native turn did not block account switch")
	}
	cancel()
	if err = bundles.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = bundles.Resolve("codex", old.Version); err != nil {
		t.Fatal("GC removed a live native bundle", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	for {
		live, e := processguard.Probe(ref)
		if e == nil && !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native group survived runner SIGKILL", live, e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pidRaw, err := os.ReadFile(ready)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("backgrounded grandchild survived cleanup")
	}
	release, err := accounts.switchGate(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, running := accounts.SwitchStatus("codex"); running != 0 {
		t.Fatal("stale native turn retained the account lease", running)
	}
	if err = bundles.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = bundles.Resolve("codex", old.Version); err == nil {
		t.Fatal("dead native process retained old bundle")
	}
}
