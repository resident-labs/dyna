package runtime

import (
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
