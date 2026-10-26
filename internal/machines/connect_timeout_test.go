package machines

import (
	"context"
	"io"
	"slices"
	"testing"
	"time"
)

// A machine behind a slow jump host stores its own connect timeout, and the
// probe passes it to ssh and waits long enough for it (D-195, D-197).
func TestProbeUsesMachineConnectTimeout(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	pve, err := s.Save(ctx, Machine{Name: "pve", Roles: []Role{RoleCI}, SSH: "pip@pve", CISlots: 2, Enabled: true, SSHConnectTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Get(ctx, "pve")
	if err != nil {
		t.Fatal(err)
	}
	if read.SSHConnectTimeout != 60*time.Second || pve.SSHConnectTimeout != 60*time.Second {
		t.Fatalf("stored connect timeout = %s, want 60s", read.SSHConnectTimeout)
	}

	var args []string
	var deadline time.Duration
	command := func(ctx context.Context, _ string, _ io.Reader, w, _ io.Writer, _ string, got ...string) error {
		args = got
		at, _ := ctx.Deadline()
		deadline = time.Until(at)
		_, err := io.WriteString(w, probeText)
		return err
	}
	if _, err := (Prober{Store: s, Command: command}).ProbeAndSave(ctx, read); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "ConnectTimeout=60") {
		t.Fatalf("ssh args %v, want ConnectTimeout=60", args)
	}
	if deadline < 60*time.Second {
		t.Fatalf("probe budget = %s, want more than the connect timeout", deadline)
	}
}

func TestConnectTimeoutDefaultsAndLimits(t *testing.T) {
	if got := SSHConnectTimeoutOption(0); got != "ConnectTimeout=10" {
		t.Fatalf("default option = %q", got)
	}
	if got := SSHConnectTimeoutOption(1500 * time.Millisecond); got != "ConnectTimeout=2" {
		t.Fatalf("rounded option = %q", got)
	}
	m := Machine{Name: "pve", Roles: []Role{RoleAgents}, SSH: "pip@pve", SSHConnectTimeout: -time.Second}
	if err := m.Validate(); err == nil {
		t.Fatal("a negative connect timeout was accepted")
	}
	m.SSHConnectTimeout = MaxSSHConnectTimeout + time.Second
	if err := m.Validate(); err == nil {
		t.Fatal("a connect timeout above the limit was accepted")
	}
}

// A machine stores the folder that holds its host checks' temporary folders,
// and refuses one that is not a clean absolute path.
func TestMachineStoresCheckTmp(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	if _, err := s.Save(ctx, Machine{Name: "pve", Roles: []Role{RoleCI}, SSH: "pip@pve", CISlots: 2, Enabled: true, CheckTmp: "/dev/shm"}); err != nil {
		t.Fatal(err)
	}
	read, err := s.Get(ctx, "pve")
	if err != nil || read.CheckTmp != "/dev/shm" {
		t.Fatalf("stored check tmp = %q, %v; want /dev/shm", read.CheckTmp, err)
	}
	for _, bad := range []string{"relative/dir", "/", "/a/../b", "/a//b"} {
		m := Machine{Name: "pve", Roles: []Role{RoleAgents}, SSH: "pip@pve", CheckTmp: bad}
		if err := m.Validate(); err == nil {
			t.Fatalf("check tmp %q was accepted", bad)
		}
	}
}
