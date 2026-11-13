package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

func TestEngineHostScopeRunnerUsesSSHWithOwnerEnvironment(t *testing.T) {
	yes, no := true, false
	engineHost, other := &podman.Host{}, &podman.Host{}
	b := &Boxes{Runtime: t.TempDir(), Store: restoreStore{hosts: []box.Host{{Name: "engine", EngineHost: &yes}, {Name: "other", EngineHost: &no}}}, hosts: map[contract.HostName]*podman.Host{"engine": engineHost, "other": other}}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []contract.HostName{"engine", contract.ControlPlane} {
		r, err := b.scopeRunner(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		remote, ok := r.(engineScopeRunner)
		if !ok || remote.Host != engineHost {
			t.Fatalf("%s runner = %#v, want engine SSH runner", name, r)
		}
		want := []string{"DYNA_SOCKET=" + filepath.Join(b.Runtime, "owner.sock"), "PATH=" + filepath.Dir(binary) + ":" + os.Getenv("PATH")}
		if !slices.Equal(remote.env, want) {
			t.Fatalf("environment = %q, want %q", remote.env, want)
		}
	}
	r, err := b.scopeRunner(context.Background(), "other")
	if err != nil || r != other {
		t.Fatalf("other runner = %#v, %v", r, err)
	}
}
