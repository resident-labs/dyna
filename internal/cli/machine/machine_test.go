package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	stubSaveMachine(t, func(context.Context, string, websvc.MachinePatch) error { return nil })
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

func stubSaveMachine(t *testing.T, save func(context.Context, string, websvc.MachinePatch) error) {
	t.Helper()
	previous := saveMachine
	saveMachine = save
	t.Cleanup(func() { saveMachine = previous })
}

func TestMachineEditHostChecksDeniedByEngine(t *testing.T) {
	storetest.DataDir(t)
	if _, err := run(t, "add", "hermes", "--ssh", "pip@hermeslab"); err != nil {
		t.Fatal(err)
	}
	called := false
	stubSaveMachine(t, func(context.Context, string, websvc.MachinePatch) error {
		called = true
		return errors.New("SaveMachine is owner only")
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
	if saved.AllowHostChecks {
		t.Fatal("denied host-check edit changed the registry")
	}
}
