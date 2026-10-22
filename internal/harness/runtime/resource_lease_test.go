package runtime

import (
	"context"
	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeResourceProbe(t *testing.T, fn func(json.RawMessage) (bool, error)) {
	t.Helper()
	old := nativeUseProbe
	nativeUseProbe = fn
	t.Cleanup(func() { nativeUseProbe = old })
}
func TestResourceProtectionOutlivesRunnerCloseUntilGuardDrains(t *testing.T) {
	testAccounts(t)
	alive := true
	fakeResourceProbe(t, func(json.RawMessage) (bool, error) { return alive, nil })
	root := filepath.Join(t.TempDir(), "uses")
	lease, err := newResourceLease(context.Background(), root, "use-", map[string]string{"resource": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Bind(json.RawMessage(`{"socket":"fixture-native-guard"}`)); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	live, _, err := resourceLeaseState(lease.path)
	if err != nil || !live {
		t.Fatal("closed runner lost live native protection", live, err)
	}
	alive = false
	count, err := resourceLeaseCount(root)
	if err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = os.Stat(lease.path); !os.IsNotExist(err) {
		t.Fatal("stale protection not collected", err)
	}
}
func TestUnknownNativeLivenessPreservesProtection(t *testing.T) {
	testAccounts(t)
	fakeResourceProbe(t, func(json.RawMessage) (bool, error) { return false, errors.New("uncertain fixture") })
	lease, err := newResourceLease(context.Background(), t.TempDir(), "use-", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Bind(json.RawMessage(`{"socket":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err == nil {
		t.Fatal("uncertain cleanup was reported as complete")
	}
	if _, err = os.Stat(lease.path); err != nil {
		t.Fatal("uncertain protection was removed", err)
	}
}
func TestAccountSwitchWaitsForGuardAfterRunnerLeaseCloses(t *testing.T) {
	a := testAccounts(t)
	alive := true
	fakeResourceProbe(t, func(json.RawMessage) (bool, error) { return alive, nil })
	lease, err := a.TurnLeaseManaged(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Bind(json.RawMessage(`{"socket":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	_, running := a.SwitchStatus("codex")
	if running != 1 {
		t.Fatal("native guard not counted", running)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if release, err := a.switchGate(ctx, "codex"); err == nil {
		release()
		t.Fatal("switch bypassed crash cleanup")
	}
	alive = false
	release, err := a.switchGate(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestSwitchActivationBoundaryIgnoresLateCancellation(t *testing.T) {
	a := testAccounts(t)
	first := signedCodex(t, a, "first", "first-fixture")
	second := signedCodex(t, a, "second", "second-fixture")
	if err := a.Switch(context.Background(), first.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		manager, ok := NativeRuntimeManager(h, host)
		return cancelDuringNativeActivation{Manager: manager, cancel: cancel}, ok
	}
	called := false
	if err := a.SwitchWithProgress(ctx, second.ID, "/fake/codex", func() { called = true }); err != nil {
		t.Fatal(err)
	}
	active, err := a.Active("codex")
	if err != nil || active.ID != second.ID || !called {
		t.Fatal(active, called, err)
	}
	before, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	called = false
	if err = a.SwitchWithProgress(before, first.ID, "/fake/codex", func() { called = true }); err == nil || called {
		t.Fatal("canceled switch crossed activation boundary", called, err)
	}
}

type cancelDuringNativeActivation struct {
	native.Manager
	cancel context.CancelFunc
}

func (m cancelDuringNativeActivation) Activate(ctx context.Context, integration string, credential store.Credential) ([]native.Captured, error) {
	m.cancel()
	return m.Manager.Activate(ctx, integration, credential)
}
