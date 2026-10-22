package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/home"
)

func TestOpenCodeUnusableNativeLoginBlocksObservation(t *testing.T) {
	a := testAccounts(t)
	scope, err := a.HarnessHome("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	if err = home.Ensure(scope); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "opencode")
	if err = fsutil.WriteExecutable(exe, []byte("#!/bin/sh\ncat \"$OPENCODE_CONFIG_DIR/status-fixture.json\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(scope, "status-fixture.json")
	ready := `[{"id":"opencode-go","connections":[{"type":"credential","id":"fixture"}]}]`
	if err = os.WriteFile(list, []byte(ready), 0600); err != nil {
		t.Fatal(err)
	}
	m, _, err := a.scopedManager("opencode", scope, exe)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := m.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 {
		t.Fatal(captured, err)
	}
	n, err := a.Create("opencode", "OpenCode Go")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(func(st *store.State) error {
		e := st.Find(n.ID)
		e.Identity, e.Integration, e.NeedsLogin = captured[0].Identity, captured[0].Integration, false
		return writeAccountLogin(n.Home, captured[0])
	}, true); err != nil {
		t.Fatal(err)
	}
	if err = a.setActive("opencode", n.ID); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: a}
	if _, err = r.observeAccount(context.Background(), n.ID, exe); err != nil {
		t.Fatal("ready login rejected", err)
	}
	if err = fsutil.WriteExecutable(exe, []byte("#!/bin/sh\necho secret-details >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.observeAccount(context.Background(), n.ID, exe); err == nil || !strings.Contains(err.Error(), "OpenCode account command failed") || strings.Contains(err.Error(), "secret-details") {
		t.Fatalf("probe failure was hidden or leaked native output: %v", err)
	}
	if err = fsutil.WriteExecutable(exe, []byte("#!/bin/sh\ncat \"$OPENCODE_CONFIG_DIR/status-fixture.json\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`[{"id":"opencode-go","connections":[{"type":"credential","id":"fixture","status":{"code":"needs_auth"}}]}]`, `[]`} {
		if err = os.WriteFile(list, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = r.observeAccount(context.Background(), n.ID, exe); !errors.Is(err, native.ErrNotSignedIn) {
			t.Fatalf("unusable login lost its sign-in error: %v", err)
		}
		saved, err := readAccountLogin(n.Home)
		if err != nil || saved.Identity != captured[0].Identity {
			t.Fatal("saved identity changed", err)
		}
	}
}

type captureFailureManager struct {
	native.Manager
	err error
}

func (m captureFailureManager) Capture(context.Context, string) ([]native.Captured, error) {
	return nil, m.err
}

func TestNativeProbeFailureDoesNotRequestSignIn(t *testing.T) {
	r, p := runtimeFixture(t, "codex")
	probeErr := context.DeadlineExceeded
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		manager, ok := NativeRuntimeManager(h, host)
		return captureFailureManager{Manager: manager, err: probeErr}, ok
	}
	if _, _, err := r.Prepare(context.Background(), p, nil); !errors.Is(err, probeErr) || strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("prepare hid probe failure: %v", err)
	}
	doctor := r.Doctor(context.Background(), p)
	if doctor.Status != "blocked" || !strings.Contains(doctor.Reason, probeErr.Error()) {
		t.Fatalf("doctor asked for sign-in after probe failure: %+v", doctor)
	}
}
