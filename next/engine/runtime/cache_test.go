package runtime

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"reflect"
	"testing"
)

func TestContainerSharesDependencyCaches(t *testing.T) {
	b := &Boxes{HostData: "/host"}
	_, c := b.container(lifecycle.BoxSpec{}, "image")
	want := box.Mount{HostPath: "/host/cache", BoxPath: "/box/cache"}
	found := false
	for _, mount := range c.Mounts {
		if mount == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("mounts: %#v", c.Mounts)
	}
	env := []box.EnvVar{{Name: "GOMODCACHE", Value: "/box/cache/gomod"}, {Name: "GOCACHE", Value: "/box/cache/gocache"}, {Name: "CARGO_HOME", Value: "/box/cache/cargo"}, {Name: "npm_config_store_dir", Value: "/box/cache/pnpm"}}
	if !reflect.DeepEqual(c.Env, env) {
		t.Fatalf("env: %#v", c.Env)
	}
}
