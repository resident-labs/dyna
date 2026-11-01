package podman

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestPushCacheUsesMaster(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	for i := 0; i < 2; i++ {
		if err := h.PushCache(context.Background(), "/mirror", "/data/git/owner/repo.git", []string{"refs/heads/main"}); err != nil {
			t.Fatal(err)
		}
		init := r.calls[i*3+1]
		if !strings.HasSuffix(init[len(init)-1], `'dyna' '/data/git/owner/repo.git'`) || !strings.Contains(init[len(init)-1], `if [ ! -d "$1" ]; then git init --bare -- "$1" && git -C "$1" config gc.auto 0; fi`) {
			t.Fatalf("init: %v", init)
		}
		want := []string{"env", "GIT_SSH_COMMAND=ssh -S " + quote(h.socket) + " -o ControlMaster=no", "git", "-C", "/mirror", "push", "--force", "user@host:/data/git/owner/repo.git", "refs/heads/main"}
		if !reflect.DeepEqual(r.calls[i*3+2], want) {
			t.Fatalf("push: %v", r.calls[i*3+2])
		}
	}
	if err := h.PushCache(context.Background(), "/mirror", "/data:/repo.git", nil); err == nil {
		t.Fatal("accepted a path that git would read as a host")
	}
}

func TestCreateContainerCreatesWritableMount(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "a", Image: "image", Mounts: []box.Mount{{HostPath: "/cache", BoxPath: "/box/cache"}}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := r.calls[1][len(r.calls[1])-1]
	if !strings.Contains(cmd, `mkdir -p -m 0700 -- "$1"`) || !strings.HasSuffix(cmd, "'dyna' '/cache'") {
		t.Fatal(cmd)
	}
}
