package runtime

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestContainerMountsRoomSharedReadWrite(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	want := box.Mount{HostPath: "/host/share/rooms/room/shared", BoxPath: "/box/shared", OnMount: "/host/share/rooms"}
	_, c := b.container(lifecycle.BoxSpec{Room: "room"}, "image", want)
	found := false
	for _, mount := range c.Mounts {
		if mount == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("mounts: %#v", c.Mounts)
	}
}

func TestContainerSharesDependencyCaches(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	_, c := b.container(lifecycle.BoxSpec{}, "image", box.Mount{})
	for _, want := range []box.Mount{
		{HostPath: "/host/cache", BoxPath: "/box/cache"},
		{HostPath: "/host/sock/build-cache", BoxPath: "/run/dyna/build-cache", ReadOnly: true, MakeDir: true},
	} {
		if !slices.Contains(c.Mounts, want) {
			t.Fatalf("mounts miss %#v: %#v", want, c.Mounts)
		}
	}
	env := []box.EnvVar{{Name: "GOMODCACHE", Value: "/box/cache/gomod"}, {Name: "GOCACHE", Value: "/box/cache/gocache"}, {Name: "CARGO_HOME", Value: "/box/cache/cargo"}, {Name: "npm_config_store_dir", Value: "/box/cache/pnpm"}}
	if !reflect.DeepEqual(c.Env, env) {
		t.Fatalf("env: %#v", c.Env)
	}
}

// A box of a repository builds where that repository's CI runners do (box/podman/runner.go), so the compiler cache's keys match.
func TestContainerBuildsWhereARunnerOfItsRepositoryDoes(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	_, c := b.container(lifecycle.BoxSpec{Repos: []contract.BoxRepo{{Name: "owner/repo"}, {Name: "owner/other"}}}, "image", box.Mount{})
	var cargo []box.EnvVar
	for _, v := range c.Env {
		if strings.HasPrefix(v.Name, "CARGO_") {
			cargo = append(cargo, v)
		}
	}
	want := []box.EnvVar{{Name: "CARGO_HOME", Value: "/box/cache/cargo"}, {Name: "CARGO_TARGET_DIR", Value: "/box/work/owner/repo/target"}}
	if !reflect.DeepEqual(cargo, want) {
		t.Fatalf("cargo env: %#v", cargo)
	}
}

func TestContainerCarriesTheAssignedGPUCard(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	card := 1
	_, c := b.container(lifecycle.BoxSpec{GPUCard: &card}, "image", box.Mount{})
	if c.GPUCard == nil || *c.GPUCard != 1 {
		t.Fatalf("card %v", c.GPUCard)
	}
	if _, c := b.container(lifecycle.BoxSpec{}, "image", box.Mount{}); c.GPUCard != nil {
		t.Fatalf("a box with no GPU got card %d", *c.GPUCard)
	}
}

func TestContainerSeesItsSizeLimits(t *testing.T) {
	b := &Boxes{}
	size, _ := box.Size(contract.BoxSizeAgent)
	slice, c := b.container(lifecycle.BoxSpec{Size: size}, "image", box.Mount{})
	if c.CPUs != 1 || c.MemBytes != slice.MemMax {
		t.Fatalf("container limits %d CPUs, %d bytes; want 1 and the slice's %d", c.CPUs, c.MemBytes, slice.MemMax)
	}
	if !slices.Contains(c.Env, box.EnvVar{Name: "OMP_THREAD_LIMIT", Value: "1"}) {
		t.Fatalf("env %v lacks OMP_THREAD_LIMIT=1, which nproc reads", c.Env)
	}
}

func TestContainerMountsAgyFilesForAgySessionsOnly(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	has := func(c box.ContainerSpec, want box.Mount) bool {
		return slices.Contains(c.Mounts, want)
	}
	binary := box.Mount{HostPath: "/host/agy/antigravity", BoxPath: "/opt/agy/antigravity", ReadOnly: true, Present: "agy binary not installed at /host/agy/antigravity"}
	account := box.Mount{HostPath: "/host/agy/accounts/main", BoxPath: "/opt/agy/account", Present: "agy account folder missing at /host/agy/accounts/main: sign in first"}
	_, agy := b.container(lifecycle.BoxSpec{Agy: true}, "image", box.Mount{})
	if !has(agy, binary) || !has(agy, account) {
		t.Fatalf("agy mounts: %#v", agy.Mounts)
	}
	_, pig := b.container(lifecycle.BoxSpec{}, "image", box.Mount{})
	if has(pig, binary) || has(pig, account) {
		t.Fatalf("pig mounts: %#v", pig.Mounts)
	}
}

type presentHost struct {
	missing map[string]bool
	checked []string
}

func (h *presentHost) CheckPresent(_ context.Context, m box.Mount) error {
	h.checked = append(h.checked, m.HostPath)
	if h.missing[m.HostPath] {
		return box.MissingMount{Text: m.Present}
	}
	return nil
}

// A warm start of an agy box checks both sources and refuses with the create's text; a box that is not agy, or is closing, is not checked.
func TestWarmStartChecksTheAgySources(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	ctx := context.Background()
	present := &presentHost{}
	if err := b.checkAgySources(ctx, present, true); err != nil || len(present.checked) != 2 {
		t.Fatalf("present sources: %v, checked %v", err, present.checked)
	}
	for _, gone := range []struct{ path, text string }{
		{"/host/agy/antigravity", "agy binary not installed at /host/agy/antigravity"},
		{"/host/agy/accounts/main", "agy account folder missing at /host/agy/accounts/main: sign in first"},
	} {
		host := &presentHost{missing: map[string]bool{gone.path: true}}
		var missing box.MissingMount
		if err := b.checkAgySources(ctx, host, true); !errors.As(err, &missing) || missing.Text != gone.text {
			t.Fatalf("%s gone: %v", gone.path, err)
		}
	}
	skipped := &presentHost{missing: map[string]bool{"/host/agy/antigravity": true}}
	if err := b.checkAgySources(ctx, skipped, false); err != nil || len(skipped.checked) != 0 {
		t.Fatalf("a box that is not agy, or is closing: %v, checked %v", err, skipped.checked)
	}
}
