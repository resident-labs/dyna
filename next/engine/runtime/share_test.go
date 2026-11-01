package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

// engineHosts lists hosts that are the engine's own machine, which need no mount.
func engineHosts(names ...contract.HostName) []box.Host {
	yes := true
	hosts := make([]box.Host, len(names))
	for i, name := range names {
		hosts[i] = box.Host{Name: name, State: box.HostReady, EngineHost: &yes}
	}
	return hosts
}

// shareHost answers every command, and fails the one that brings up the mount when given a reason.
type shareHost struct {
	mu       sync.Mutex
	commands []string
	refuse   string
}

func (r *shareHost) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		r.commands = append(r.commands, strings.Join(args, " "))
		return nil, nil
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	if strings.Contains(command, "'dyna-share'") && r.refuse != "" {
		return []byte(fmt.Sprintf("%sDYNA-END %020d 001\n", r.refuse, len(r.refuse))), nil
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func (r *shareHost) index(part string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.IndexFunc(r.commands, func(command string) bool { return strings.Contains(command, part) })
}

func shareBoxes(t *testing.T, r podman.Runner, hosts []box.Host) *Boxes {
	t.Helper()
	dir, err := os.MkdirTemp("", "dyna-rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h, err := podman.New("user@host", dir, "host", r)
	if err != nil {
		t.Fatal(err)
	}
	return &Boxes{Store: restoreStore{hosts: hosts}, Runtime: dir, Layout: box.Layout{Data: "/data", HostData: "/host"}, hosts: map[contract.HostName]*podman.Host{"host": h}, installed: map[contract.HostName]bool{"host": true}}
}

func TestABoxOnAnotherHostBindsOnlyItsRoomThroughTheMount(t *testing.T) {
	no := false
	r := &shareHost{}
	b := shareBoxes(t, r, []box.Host{{Name: "host", State: box.HostReady, EngineHost: &no}})
	mount, err := b.sharedMount(context.Background(), "host", "room_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (box.Mount{HostPath: "/host/share/rooms/room_1/shared", BoxPath: "/box/shared", OnMount: "/host/share/rooms"}); mount != want {
		t.Fatalf("mount %+v", mount)
	}
	forward, up := r.index("/host/sock/share.sock:"), r.index("'dyna-share'")
	if forward < 0 || up < forward {
		t.Fatalf("the mount came before its socket: %q", r.commands)
	}
}

func TestTheEngineHostBindsTheCentralFolderWithNoMount(t *testing.T) {
	r := &shareHost{}
	b := shareBoxes(t, r, engineHosts("host"))
	mount, err := b.sharedMount(context.Background(), "host", "room_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (box.Mount{HostPath: "/data/rooms/room_1/shared", BoxPath: "/box/shared"}); mount != want {
		t.Fatalf("mount %+v", mount)
	}
	if len(r.commands) != 0 {
		t.Fatalf("the engine host got commands: %q", r.commands)
	}
}

func TestABoxWaitsWithAReasonWhileItsHostHasNoMount(t *testing.T) {
	no := false
	r := &shareHost{refuse: "user_allow_other is not set in /etc/fuse.conf"}
	b := shareBoxes(t, r, []box.Host{{Name: "host", State: box.HostReady, EngineHost: &no}})
	_, err := b.sharedMount(context.Background(), "host", "room_1")
	var down box.ShareDown
	if !errors.As(err, &down) || down.Host != "host" || !strings.Contains(err.Error(), r.refuse) {
		t.Fatalf("err = %v", err)
	}
}

// An engine restart loses the master that held the mount's socket. The mount itself lives on the host and must not be touched.
func TestARestartGivesARunningMountItsSocketBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newSlowHost(0)
	first := startEngine(t, ctx, r, restoreFixture("box_a"))
	eventually(t, "the socket", func() bool { return r.restored("box_a", "share.sock") })
	cancel()
	first.WaitRecovered()
	r.restart()
	ctx, cancel = context.WithCancel(context.Background())
	second := startEngine(t, ctx, r, restoreFixture("box_a"))
	defer func() { cancel(); second.WaitRecovered() }()
	eventually(t, "the socket after a restart", func() bool { return r.restored("box_a", "share.sock") })
	if n := r.count("share mount"); n != 0 {
		t.Fatalf("recovery restarted the mount %d times, which would cut off running boxes", n)
	}
}

func TestTheEngineHostNeedsNoSocketForTheMount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newSlowHost(0)
	st := restoreStore{hosts: engineHosts("arch"), boxes: []box.Box{{ID: "box_a", Session: "ses_box_a", Host: "arch", State: contract.BoxReady}}}
	b := startEngine(t, ctx, r, st)
	defer func() { cancel(); b.WaitRecovered() }()
	eventually(t, "the box's socket", func() bool { return r.restored("box_a") })
	if r.restored("share.sock") {
		t.Fatal("the engine host got the mount's socket")
	}
}

func TestSharedSizesAddUpEachRoomsFolder(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	for path, size := range map[string]int{filepath.Join(layout.Shared("a"), "one"): 3, filepath.Join(layout.Shared("a"), "sub", "two"): 4, filepath.Join(layout.Shared("b"), "three"): 5} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(layout.Files("empty"), 0700); err != nil {
		t.Fatal(err)
	}
	sizes, err := sharedSizes(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	if want := []contract.RoomShared{{Room: "a", Bytes: 7}, {Room: "b", Bytes: 5}}; !slices.Equal(sizes, want) {
		t.Fatalf("sizes %+v", sizes)
	}
}

func TestSharedSizesReadsNoRoomsBeforeTheFirst(t *testing.T) {
	sizes, err := sharedSizes(context.Background(), box.Layout{Data: filepath.Join(t.TempDir(), "none")})
	if err != nil || len(sizes) != 0 {
		t.Fatalf("sizes %+v, err %v", sizes, err)
	}
}

// measureSharedStore records what one measure stores.
type measureSharedStore struct {
	restoreStore
	got chan []contract.RoomShared
}

func (s measureSharedStore) SetRoomShared(_ context.Context, sizes []contract.RoomShared, _ time.Time) error {
	s.got <- sizes
	return nil
}

func TestMeasureSharedStoresWhenTheEngineStarts(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	if err := os.MkdirAll(layout.Shared("a"), 0700); err != nil {
		t.Fatal(err)
	}
	st := measureSharedStore{got: make(chan []contract.RoomShared, 1)}
	b := &Boxes{Store: st, Layout: layout}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.measureShared(ctx)
	select {
	case sizes := <-st.got:
		if want := []contract.RoomShared{{Room: "a"}}; !slices.Equal(sizes, want) {
			t.Fatalf("sizes %+v", sizes)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was measured")
	}
}
