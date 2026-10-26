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

func TestMachineCommandsChangeTheRegistry(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	ctx := context.Background()
	localName := localMachineName(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab", "--ci-slots", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab", "--ci-slots", "3"); err == nil || !strings.Contains(err.Error(), "machine hermes exists; use dyna machine edit") {
		t.Fatalf("second add = %v", err)
	}
	if _, err := run(t, "add", "bad", "--ssh", "user@host; id"); err == nil {
		t.Fatal("accepted SSH shell syntax")
	}
	if _, err := run(t, "add", "build", "--ssh", "ari@build", "--role", "agents"); err != nil {
		t.Fatal(err)
	}
	list := listed(t)
	if local, ok := find(list, localName); !ok || !local.Local {
		t.Fatalf("list = %#v", list)
	}
	hermes, ok := find(list, "hermes")
	if !ok || !hermes.HasRole(machines.RoleCI) || hermes.CISlots != 2 || hermes.SSH != "pip@hermeslab" {
		t.Fatalf("hermes = %#v", hermes)
	}
	build, ok := find(list, "build")
	if !ok || !build.HasRole(machines.RoleAgents) || build.CISlots != 0 || build.HasRole(machines.RoleCI) {
		t.Fatalf("build = %#v", build)
	}
	// --ci-slots gives this machine the ci role, and --role replaces roles.
	if _, err := run(t, "edit", localName, "--ci-slots", "4"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "build", "--role", "ci", "--ssh", "ari@build"); err != nil {
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
	if _, err := run(t, "edit", "hermes", "--rename", "hermeslab", "--disable"); err != nil {
		t.Fatal(err)
	}
	list = listed(t)
	if local, _ := find(list, localName); !local.HasRole(machines.RoleCI) || local.CISlots != 4 {
		t.Fatalf("local machine = %#v", local)
	}
	if build, _ := find(list, "build"); build.HasRole(machines.RoleAgents) || build.CISlots != 1 {
		t.Fatalf("replaced roles = %#v", build)
	}
	renamed, ok := find(list, "hermeslab")
	if !ok || renamed.ID != hermes.ID || renamed.Enabled || renamed.CISlots != 2 {
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

// A machine that gains the ci role in the same command takes the host-check
// permission with it, instead of losing it to the role save.
func TestMachineEditTakesCIRoleAndHostChecksTogether(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "build", "--ssh", "ari@build", "--role", "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "build", "--ci-slots", "1", "--allow-host-checks"); err != nil {
		t.Fatal(err)
	}
	build, err := (machines.Store{}).Get(context.Background(), "build")
	if err != nil {
		t.Fatal(err)
	}
	if !build.HasRole(machines.RoleCI) || build.CISlots != 1 || !build.HostChecksAllowed("room_dyna") {
		t.Fatalf("edited machine = %#v, want the ci role and the room's host checks", build)
	}
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
	if _, err := run(t, "add", "beta", "--ssh", "pip@beta", "--ci-slots", "1"); err != nil {
		t.Fatal(err)
	}
	alpha, err := (machines.Store{}).Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	// A machine may take a name that is another machine's ID.
	if _, err := run(t, "edit", "beta", "--rename", alpha.ID, "--ci-slots", "2"); err != nil {
		t.Fatal(err)
	}
	// Naming that machine by its new name now edits it, not alpha.
	if _, err := run(t, "edit", alpha.ID, "--ci-slots", "5"); err != nil {
		t.Fatal(err)
	}
	after, err := (machines.Store{}).Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if after.CISlots != 1 {
		t.Fatalf("alpha slots = %d, want 1: the edit retargeted the machine that owns its ID", after.CISlots)
	}
	list := listed(t)
	var renamed machines.Machine
	for _, m := range list {
		if m.Name == alpha.ID {
			renamed = m
		}
	}
	if renamed.CISlots != 5 {
		t.Fatalf("renamed machine slots = %d, want 5", renamed.CISlots)
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

// A machine can be limited to chosen suites, and the limit is shown and can be
// cleared again. An empty list keeps the default of every suite.
func TestMachineEditLimitsSuites(t *testing.T) {
	storetest.DataDir(t)
	stubEngine(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab", "--ci-slots", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "hermes", "--suites", "go,frontend"); err != nil {
		t.Fatal(err)
	}
	hermes, ok := find(listed(t), "hermes")
	if !ok || !reflect.DeepEqual(hermes.Suites, []string{"frontend", "go"}) {
		t.Fatalf("suite limit = %v", hermes.Suites)
	}
	if out, err := run(t, "show", "hermes"); err != nil || !strings.Contains(out, "Suites:   frontend, go") {
		t.Fatalf("shown suite limit = %q, %v", out, err)
	}
	if _, err := run(t, "edit", "hermes", "--suites", ""); err != nil {
		t.Fatal(err)
	}
	hermes, _ = find(listed(t), "hermes")
	if len(hermes.Suites) != 0 || !hermes.AllowsSuite("relay") {
		t.Fatalf("cleared suite limit = %v", hermes.Suites)
	}
	if out, err := run(t, "show", "hermes"); err != nil || !strings.Contains(out, "Suites:   every suite") {
		t.Fatalf("shown cleared limit = %q, %v", out, err)
	}
	// A suite limit belongs to the CI role, and taking that role away clears it.
	if _, err := run(t, "add", "build", "--ssh", "ari@build", "--role", "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "build", "--suites", "go"); err == nil {
		t.Fatal("gave a machine without the ci role a suite limit")
	}
	if _, err := run(t, "edit", "hermes", "--suites", "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "edit", "hermes", "--role", "agents"); err != nil {
		t.Fatal(err)
	}
	if hermes, _ := find(listed(t), "hermes"); len(hermes.Suites) != 0 {
		t.Fatalf("suite limit survived the ci role: %v", hermes.Suites)
	}
}
