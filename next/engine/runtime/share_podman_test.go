package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// shareSeen is how long a change may take to reach the other side: the mount's 5-second listing, then a margin.
const shareSeen = 15 * time.Second

// A box on a host that is not the engine's, with its real mount, its real server and real podman. DYNA_SHARE_TEST_BINARY names a
// dyna build. DYNA_SHARE_TEST_IMAGE names a small local image with /usr/bin/tini. The host needs FUSE with user_allow_other, and
// the test uses the user service dyna-share.service, so it must not run where a real mount is up. sshForwards stands in for
// the SSH master, so a dropped forward here is one the engine would lose by restarting.
func TestSharedFolderReachesARealBoxThroughItsMount(t *testing.T) {
	binary, image := os.Getenv("DYNA_SHARE_TEST_BINARY"), os.Getenv("DYNA_SHARE_TEST_IMAGE")
	if binary == "" || image == "" {
		t.Skip("DYNA_SHARE_TEST_BINARY and DYNA_SHARE_TEST_IMAGE name a dyna build and an image with tini; this test runs real podman and FUSE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	data, err := os.MkdirTemp("", "dyna-share-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", data).Run() })
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	runtimeDir := filepath.Join(data, "rt")
	for _, dir := range []string{layout.Rooms(), runtimeDir, filepath.Dir(layout.HostBinary()), layout.HostCache()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// A box's agent stays out of this: only the mount needs the real binary.
	wrapper := "#!/bin/sh\nif [ \"$1\" = share ]; then exec " + binary + " \"$@\"; fi\nexec sleep 1000\n"
	if err := os.WriteFile(layout.HostBinary(), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	forwards := &sshForwards{}
	host, err := podman.New("local", runtimeDir, "h", forwards)
	if err != nil {
		t.Fatal(err)
	}
	no := false
	b := &Boxes{Store: restoreStore{hosts: []box.Host{{Name: "h", State: box.HostReady, EngineHost: &no}}}, Layout: layout, Runtime: runtimeDir, Context: ctx,
		hosts: map[contract.HostName]*podman.Host{"h": host}, restored: map[contract.HostName]bool{"h": true}, installed: map[contract.HostName]bool{"h": true}}
	serve := func() {
		t.Helper()
		if err := podman.EnsureShareServer(ctx, binary, layout.Rooms(), b.shareSocket()); err != nil {
			t.Fatal(err)
		}
	}
	serve()
	stopServer := func() { _ = exec.Command("systemctl", "--user", "stop", "dyna-share-server.service").Run() }
	t.Cleanup(stopServer)
	stopMount := func() { _ = exec.Command("systemctl", "--user", "stop", "dyna-share.service").Run() }
	t.Cleanup(stopMount)

	room, other := contract.RoomID("room_a"), contract.RoomID("room_b")
	if err := os.MkdirAll(layout.Shared(other), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Shared(other), "secret"), []byte("another room"), 0600); err != nil {
		t.Fatal(err)
	}
	shared, err := b.sharedMount(ctx, "h", room)
	if err != nil {
		t.Fatal(err)
	}
	if shared.HostPath != layout.HostShared(room) {
		t.Fatalf("mount %+v", shared)
	}
	id := contract.BoxID(fmt.Sprintf("box_share_test_%d", time.Now().UnixNano()))
	slice := "dyna-share-test-" + strings.TrimPrefix(string(id), "box_share_test_") + ".slice"
	spec := lifecycle.BoxSpec{Box: id, Room: room, Host: "h", Slice: slice, Size: contract.BoxSize{CPUs: 1, MemBytes: 1 << 30, Pids: 512}}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
		_ = host.RemoveSlice(context.Background(), slice)
	})
	if err := os.MkdirAll(layout.HostSocketFolder(id), 0700); err != nil {
		t.Fatal(err)
	}
	sliceSpec, container := b.container(spec, image, shared)
	if err := host.EnsureSlice(ctx, sliceSpec); err != nil {
		t.Fatal(err)
	}
	if err := host.CreateContainer(ctx, container); err != nil {
		t.Fatal(err)
	}
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	inBox := func(script string) (string, error) {
		out, err := exec.CommandContext(ctx, "podman", "exec", string(id), "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	must := func(script string) string {
		t.Helper()
		out, err := inBox(script)
		if err != nil {
			t.Fatalf("%s: %v: %s", script, err, out)
		}
		return out
	}
	soon := func(what string, ok func() bool) {
		t.Helper()
		start := time.Now()
		for !ok() {
			if time.Since(start) > shareSeen {
				t.Fatalf("%s: not within %s", what, shareSeen)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("%s: %s", what, time.Since(start).Round(100*time.Millisecond))
	}
	engineHas := func(name, want string) func() bool {
		return func() bool {
			got, err := os.ReadFile(filepath.Join(layout.Shared(room), name))
			return err == nil && string(got) == want
		}
	}
	boxHas := func(name, want string) func() bool {
		return func() bool {
			out, err := inBox("cat /box/shared/" + name)
			return err == nil && out == want
		}
	}

	must("echo -n from-box > /box/shared/from-box")
	soon("a box's write reaches the engine host", engineHas("from-box", "from-box"))
	if err := os.WriteFile(filepath.Join(layout.Shared(room), "from-engine"), []byte("from-engine"), 0600); err != nil {
		t.Fatal(err)
	}
	soon("the engine host's write reaches the box", boxHas("from-engine", "from-engine"))
	must(`printf '#!/bin/sh\necho ran' > /box/shared/script; chmod +x /box/shared/script; flock /box/shared/script true`)
	soon("a script runs from the mount", func() bool { out, err := inBox("/box/shared/script"); return err == nil && out == "ran" })

	if listed := must("ls -a /box/shared/.. | tr '\\n' ' '"); strings.Contains(listed, string(other)) || strings.Contains(listed, string(room)) {
		t.Fatalf("the box sees the rooms: %s", listed)
	}
	if out, err := inBox("cat /box/shared/../" + string(other) + "/shared/secret"); err == nil {
		t.Fatalf("the box read another room: %s", out)
	}
	if line := must("grep ' /box/shared ' /proc/self/mountinfo"); strings.Contains(line, string(other)) || !strings.Contains(line, " /"+string(room)+"/shared ") {
		t.Fatalf("the box binds more than its room's folder: %s", line)
	}

	// An engine restart: the forward goes, and past the mount's 5-second listing a box's next call has to wait for it.
	forwards.close(layout.HostShareSocket())
	time.Sleep(7 * time.Second)
	held := make(chan error, 1)
	go func() {
		out, err := inBox("cat /box/shared/from-box && ls /box/shared >/dev/null && echo -n written-while-down > /box/shared/while-down")
		if err == nil && out != "from-box" {
			err = fmt.Errorf("read %q", out)
		}
		held <- err
	}()
	time.Sleep(8 * time.Second)
	if err := forwards.forward(layout.HostShareSocket(), b.shareSocket()); err != nil {
		t.Fatal(err)
	}
	if err := <-held; err != nil {
		t.Fatalf("a box's call failed across the restart: %v", err)
	}
	soon("a write held through the restart reaches the engine host", engineHas("while-down", "written-while-down"))

	// The server stops and comes back with the forward in place, and the mount picks it up.
	stopServer()
	time.Sleep(time.Second)
	serve()
	soon("the mount reaches the server again", func() bool {
		_, err := inBox("echo -n after-the-server > /box/shared/after-server")
		return err == nil
	})
	soon("a write after the server came back reaches the engine host", engineHas("after-server", "after-the-server"))
	if err := os.WriteFile(filepath.Join(layout.Shared(room), "after"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	soon("the running box sees new files after the restart", boxHas("after", "after"))
	must("echo -n still-writing > /box/shared/still")
	soon("the running box still writes after the restart", engineHas("still", "still-writing"))

	// The mount goes down, so nothing may start on the empty folder under it.
	if err := host.StopContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	stopMount()
	refused := box.Mount{HostPath: layout.HostShared("room_new"), BoxPath: "/box/shared", OnMount: layout.HostShareMount()}
	container.Mounts = []box.Mount{refused}
	var down box.ShareDown
	if err := host.CreateContainer(ctx, container); !errors.As(err, &down) {
		t.Fatalf("a box was created on a down mount: %v", err)
	}
	if err := host.StartContainer(ctx, id); err == nil {
		t.Fatal("a box started on a down mount")
	}
	entries, err := os.ReadDir(layout.HostShareMount())
	if err != nil || len(entries) != 0 {
		t.Fatalf("the folder under the mount was written: %v %v", entries, err)
	}

	// The next start brings it back, and the box finds its files.
	if _, err := b.shareReady(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	soon("the box finds its files after the mount returns", boxHas("still", "still-writing"))
}
