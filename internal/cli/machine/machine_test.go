package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func listed(t *testing.T) []machines.Machine {
	t.Helper()
	out, err := run(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list []machines.Machine
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list JSON %q: %v", out, err)
	}
	return list
}

func find(list []machines.Machine, name string) (machines.Machine, bool) {
	for _, m := range list {
		if m.Name == name {
			return m, true
		}
	}
	return machines.Machine{}, false
}

func TestMachineListShowsRooms(t *testing.T) {
	storetest.DataDir(t)
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "dyna"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeTable(&out, []machines.Machine{{Name: "hermeslab", SSH: "pip@hermeslab", Rooms: []string{rec.ID}, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ROOMS", "pip@hermeslab", "dyna"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("machine list misses %q:\n%s", want, out.String())
		}
	}
}

// The ci label says a machine runs local CI checks: edit adds and removes
// labels, show lists them, and an invalid name is rejected.
func TestMachineEditSetsLabels(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "build", "--ssh", "pip@build"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "build", "--label", "ci"); err != nil {
		t.Fatal(err)
	}
	m, err := (machines.Store{}).Get(context.Background(), "build")
	if err != nil || !m.HasLabel(machines.LabelCI) {
		t.Fatalf("labeled machine = %#v, %v", m, err)
	}
	if out, err := run(t, "show", "build"); err != nil || !strings.Contains(out, "Owner labels:  ci") {
		t.Fatalf("show labels = %q, %v", out, err)
	}
	if _, err := run(t, "edit", "build", "--unlabel", "ci"); err != nil {
		t.Fatal(err)
	}
	if m, err := (machines.Store{}).Get(context.Background(), "build"); err != nil || m.HasLabel(machines.LabelCI) {
		t.Fatalf("unlabeled machine = %#v, %v", m, err)
	}
	if _, err := run(t, "edit", "build", "--label", "not a label!"); err == nil {
		t.Fatal("an invalid label was accepted")
	}
}

// The owner sets a machine's ssh connect timeout with edit and reads it back
// with show; zero restores the default, and a value ssh cannot read is refused.
func TestMachineEditSetsSSHConnectTimeout(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "pve", "--ssh", "pip@pve"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "show", "pve"); err != nil || !strings.Contains(out, "SSH connect timeout: 10s") {
		t.Fatalf("default timeout = %q, %v", out, err)
	}
	if _, err := run(t, "edit", "pve", "--ssh-connect-timeout", "60s"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "show", "pve"); err != nil || !strings.Contains(out, "SSH connect timeout: 1m0s") {
		t.Fatalf("timeout after edit = %q, %v", out, err)
	}
	for _, bad := range []string{"-5s", "1500ms", "2h"} {
		if _, err := run(t, "edit", "pve", "--ssh-connect-timeout", bad); err == nil {
			t.Fatalf("timeout %s was accepted", bad)
		}
	}
	if _, err := run(t, "edit", "pve", "--ssh-connect-timeout", "0"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "show", "pve"); err != nil || !strings.Contains(out, "SSH connect timeout: 10s") {
		t.Fatalf("timeout after reset = %q, %v", out, err)
	}
}

// Named resource counts round-trip through edit and show. A zero count
// removes one declaration without replacing the others.
func TestMachineEditResources(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "hermes", "--resource", "ghidra=1", "--resource", "gpu-slot=2", "--resource", "display=1"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "show", "hermes")
	if err != nil || !strings.Contains(out, "Resources: display=1, ghidra=1, gpu-slot=2") {
		t.Fatalf("resource show = %q, %v", out, err)
	}
	if _, err := run(t, "edit", "hermes", "--resource", "display=0", "--resource", "ghidra=3"); err != nil {
		t.Fatal(err)
	}
	stored, err := (machines.Store{}).Get(context.Background(), "hermes")
	if err != nil {
		t.Fatal(err)
	}
	want := map[machines.ResourceName]int{"ghidra": 3, "gpu-slot": 2}
	if !reflect.DeepEqual(stored.Resources, want) {
		t.Fatalf("stored resources = %v, want %v", stored.Resources, want)
	}
	for _, bad := range []string{"Ghidra=1", "ghidra=-1", "ghidra", "bad/name=1"} {
		if _, err := run(t, "edit", "hermes", "--resource", bad); err == nil {
			t.Fatalf("resource edit %q was accepted", bad)
		}
	}
}

func TestMachineCommandsChangeTheRegistry(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	ctx := context.Background()
	localName := localMachineName(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err == nil || !strings.Contains(err.Error(), "machine hermes exists; use dyna machine edit") {
		t.Fatalf("second add = %v", err)
	}
	if _, err := run(t, "add", "bad", "--ssh", "user@host; id"); err == nil {
		t.Fatal("accepted SSH shell syntax")
	}
	if _, err := run(t, "add", "build", "--ssh", "ari@build"); err != nil {
		t.Fatal(err)
	}
	list := listed(t)
	if local, ok := find(list, localName); !ok || !local.Local {
		t.Fatalf("list = %#v", list)
	}
	hermes, ok := find(list, "hermes")
	if !ok || hermes.SSH != "pip@hermeslab" || !hermes.Enabled {
		t.Fatalf("hermes = %#v", hermes)
	}
	build, ok := find(list, "build")
	if !ok || build.SSH != "ari@build" || !build.Enabled {
		t.Fatalf("build = %#v", build)
	}
	if _, err := run(t, "edit", localName, "--max-io-heavy", "4"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "build", "--ssh", "ari@build"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "hermes", "--allow-host-checks"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "show", "hermes"); err != nil || !strings.Contains(out, "Host checks: true") {
		t.Fatalf("host-check setting = %q, %v", out, err)
	}
	// A machine that cannot run Docker names its own runtime socket, and a
	// value that is not a unix socket is refused before anything is saved.
	if _, err := run(t, "edit", "hermes", "--container-socket", "tcp://127.0.0.1:2375"); err == nil || !strings.Contains(err.Error(), "must be a unix:// path") {
		t.Fatalf("a tcp socket = %v", err)
	}
	if _, err := run(t, "edit", "hermes", "--container-socket", "unix:///run/user/1000/podman/podman.sock"); err != nil {
		t.Fatal(err)
	}
	if socketMachine, _ := find(listed(t), "hermes"); socketMachine.ContainerSocket != "unix:///run/user/1000/podman/podman.sock" {
		t.Fatalf("container socket = %q", socketMachine.ContainerSocket)
	}
	if _, err := run(t, "edit", "hermes", "--rename", "buildhost", "--disable"); err != nil {
		t.Fatal(err)
	}
	list = listed(t)
	if local, _ := find(list, localName); local.MaxIOHeavy != 4 {
		t.Fatalf("local machine = %#v", local)
	}
	if build, _ := find(list, "build"); build.SSH != "ari@build" {
		t.Fatalf("edited machine = %#v", build)
	}
	renamed, ok := find(list, "buildhost")
	if !ok || renamed.ID != hermes.ID || renamed.Enabled {
		t.Fatalf("renamed machine = %#v", renamed)
	}
	if _, err := run(t, "rm", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "rm", localName); err == nil {
		t.Fatal("removed this machine")
	}
	if _, ok := find(listed(t), "build"); ok {
		t.Fatal("removed machine remains")
	}
	if _, err := (machines.Store{}).Get(ctx, "hermes"); err == nil {
		t.Fatal("old name remains")
	}
}

// localMachineName returns the name of this machine's row.
func localMachineName(t *testing.T) string {
	t.Helper()
	local, err := (machines.Store{}).GetLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return local.Name
}

// stubEngine sends a change through the engine's machine service in process,
// as the engine does, so a test sees the rules the owner sees.
func stubEngine(t *testing.T) {
	t.Helper()
	stubSaveMachine(t, func(ctx context.Context, ref string, patch websvc.MachinePatch) (websvc.MachineDetail, error) {
		return (websvc.MachineAPI{Store: machines.Store{}}).SaveMachine(ctx, ref, patch)
	})
}

func stubSaveMachine(t *testing.T, save func(context.Context, string, websvc.MachinePatch) (websvc.MachineDetail, error)) {
	t.Helper()
	previous := saveMachine
	saveMachine = save
	t.Cleanup(func() { saveMachine = previous })
}

func TestMachineEditHostChecksDeniedByEngine(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err != nil {
		t.Fatal(err)
	}
	called := false
	stubSaveMachine(t, func(context.Context, string, websvc.MachinePatch) (websvc.MachineDetail, error) {
		called = true
		return websvc.MachineDetail{}, errors.New("SaveMachine is owner only")
	})
	if _, err := run(t, "edit", "hermes", "--allow-host-checks"); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("session host-check edit = %v, want owner denial", err)
	}
	if !called {
		t.Fatal("host-check edit bypassed the engine")
	}
	saved, err := (machines.Store{}).Get(context.Background(), "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if saved.HasHostChecksGrants() {
		t.Fatal("denied host-check edit changed the registry")
	}
}

// The command sends every change to the engine, the one writer of the
// registry: a refused write leaves no machine behind and no field committed.
func TestMachineWriteGoesThroughTheEngine(t *testing.T) {
	storetest.DataDir(t)
	stubSaveMachine(t, func(context.Context, string, websvc.MachinePatch) (websvc.MachineDetail, error) {
		return websvc.MachineDetail{}, errors.New("SaveMachine is owner only")
	})
	if _, err := run(t, "add", "build", "--ssh", "ari@build"); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("refused add = %v, want the engine's refusal", err)
	}
	if _, err := (machines.Store{}).Get(context.Background(), "build"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused add wrote the registry: %v", err)
	}
}

// A machine name can equal another machine's ID. An edit names the machine it
// read by ID, so a collision cannot retarget the change to the other machine.
func TestMachineEditNamesTheMachineByID(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "alpha", "--ssh", "pip@alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "alpha", "--max-io-heavy", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "add", "beta", "--ssh", "pip@beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "beta", "--max-io-heavy", "1"); err != nil {
		t.Fatal(err)
	}
	alpha, err := (machines.Store{}).Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	// A machine may take a name that is another machine's ID.
	if _, err := run(t, "edit", "beta", "--rename", alpha.ID, "--max-io-heavy", "2"); err != nil {
		t.Fatal(err)
	}
	// Naming that machine by its new name now edits it, not alpha.
	if _, err := run(t, "edit", alpha.ID, "--max-io-heavy", "5"); err != nil {
		t.Fatal(err)
	}
	after, err := (machines.Store{}).Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if after.MaxIOHeavy != 1 {
		t.Fatalf("alpha limit = %d, want 1: the edit retargeted the machine that owns its ID", after.MaxIOHeavy)
	}
	list := listed(t)
	var renamed machines.Machine
	for _, m := range list {
		if m.Name == alpha.ID {
			renamed = m
		}
	}
	if renamed.MaxIOHeavy != 5 {
		t.Fatalf("renamed machine limit = %d, want 5", renamed.MaxIOHeavy)
	}
}

// Removing a machine goes through the engine too: a refused write leaves the
// machine stored, and the CI alias's remove shares that path.
func TestMachineRemoveGoesThroughTheEngine(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err != nil {
		t.Fatal(err)
	}
	stubSaveMachine(t, func(context.Context, string, websvc.MachinePatch) (websvc.MachineDetail, error) {
		return websvc.MachineDetail{}, errors.New("SaveMachine is owner only")
	})
	if _, err := run(t, "rm", "hermes"); err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Fatalf("refused remove = %v, want the engine's refusal", err)
	}
	if _, err := (machines.Store{}).Get(context.Background(), "hermes"); err != nil {
		t.Fatalf("a refused remove deleted the machine: %v", err)
	}
}

// A remove names the machine it read by ID: a name that equals another
// machine's ID must not delete that other machine.
func TestMachineRemoveNamesTheMachineByID(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "alpha", "--ssh", "pip@alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "add", "beta", "--ssh", "pip@beta"); err != nil {
		t.Fatal(err)
	}
	alpha, err := (machines.Store{}).Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "beta", "--rename", alpha.ID); err != nil {
		t.Fatal(err)
	}
	// Removing the machine named for alpha's ID removes the renamed machine.
	if _, err := run(t, "rm", alpha.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (machines.Store{}).Get(context.Background(), "alpha"); err != nil {
		t.Fatalf("the remove deleted the machine that owns the name as its ID: %v", err)
	}
	if _, err := (machines.Store{}).Get(context.Background(), alpha.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the renamed machine remains: %v", err)
	}
}
