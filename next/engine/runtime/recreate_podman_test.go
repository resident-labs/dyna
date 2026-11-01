package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// localHost runs the host commands here instead of over SSH.
type localHost struct{}

func (localHost) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") {
		return nil, nil
	}
	c := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	c.Stdin = bytes.NewReader(input)
	return c.Output()
}

func podmanCommand(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A real box swap on real podman. DYNA_PODMAN_TEST_BASE names a small local image with /usr/bin/tini: keep-id chowns the
// whole image on first use and holds the store lock for other podman users meanwhile.
func TestRecreateMovesAStoppedBoxToANewImageAndKeepsItsVolume(t *testing.T) {
	base := os.Getenv("DYNA_PODMAN_TEST_BASE")
	if base == "" {
		t.Skip("DYNA_PODMAN_TEST_BASE names a local image with tini; this test runs real podman")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	id := contract.BoxID(fmt.Sprintf("box_swap_test_%d", time.Now().UnixNano()))
	build := func(name string) string {
		dir := t.TempDir()
		file := fmt.Sprintf("FROM %s\nUSER root\nRUN mkdir -p /box && chown 1000:1000 /box && echo %s > /image-name\n", base, name)
		if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
		tag := "localhost/dyna-swap-test:" + name + strings.TrimPrefix(string(id), "box_swap_test_")
		podmanCommand(t, "build", "-q", "-t", tag, dir)
		t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", tag).Run() })
		return tag
	}
	oldImage, newImage := build("old"), build("new")

	data, err := os.MkdirTemp("", "dyna-swap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	// The volume mounts of the real spec. This agent records which image it woke on.
	agent := filepath.Join(data, "bin", "current", "dyna")
	if err := os.MkdirAll(filepath.Dir(agent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte("#!/bin/sh\ncp /image-name /box/woke-on\nexec sleep 300\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "sock", string(id)), 0755); err != nil {
		t.Fatal(err)
	}

	host, err := podman.New("local@host", t.TempDir(), "local", localHost{})
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, installed: map[contract.HostName]bool{"host": true}}
	b.Layout.HostData = data
	slice := "dyna-swap-test-" + strings.TrimPrefix(string(id), "box_swap_test_") + ".slice"
	spec := lifecycle.BoxSpec{Box: id, Room: "room", Host: "host", Slice: slice, Size: contract.BoxSize{CPUs: 1, MemBytes: 1 << 30, Pids: 512}}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
		_ = host.RemoveSlice(context.Background(), slice)
	})
	sliceSpec, container := b.container(spec, oldImage)
	if err := host.EnsureSlice(ctx, sliceSpec); err != nil {
		t.Fatal(err)
	}
	if err := host.CreateContainer(ctx, container); err != nil {
		t.Fatal(err)
	}
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	wokeOn := func(want string) {
		t.Helper()
		for ctx.Err() == nil {
			out, err := exec.Command("podman", "exec", string(id), "cat", "/box/woke-on").CombinedOutput()
			if err == nil && strings.TrimSpace(string(out)) == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("the box never woke on %s", want)
	}
	wokeOn("old")
	before := podmanCommand(t, "inspect", "--format", "{{.Id}}", string(id))
	podmanCommand(t, "exec", string(id), "sh", "-c", "echo kept > /box/work-file; echo gone > /tmp/layer-file")
	if err := host.StopContainer(ctx, id); err != nil {
		t.Fatal(err)
	}

	if err := b.Recreate(ctx, spec, newImage, slice); err != nil {
		t.Fatal(err)
	}

	wokeOn("new")
	if after := podmanCommand(t, "inspect", "--format", "{{.Id}}", string(id)); after == before {
		t.Fatal("the container was not replaced")
	}
	if got := podmanCommand(t, "inspect", "--format", "{{.ImageName}}", string(id)); got != newImage {
		t.Fatalf("image %q, want %q", got, newImage)
	}
	if got := podmanCommand(t, "exec", string(id), "cat", "/box/work-file"); got != "kept" {
		t.Fatalf("volume file %q", got)
	}
	if err := exec.Command("podman", "exec", string(id), "test", "-e", "/tmp/layer-file").Run(); err == nil {
		t.Fatal("the old container layer survived")
	}
}
