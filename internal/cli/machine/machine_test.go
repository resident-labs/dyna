package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/store/storetest"
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
