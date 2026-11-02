package runtime

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

const (
	// measureSharedEvery is how often the rooms' shared folders are measured.
	measureSharedEvery = 24 * time.Hour
	// shareServerEvery is how soon a server that stopped comes back.
	shareServerEvery = 10 * time.Second
)

func (b *Boxes) shareSocket() string { return filepath.Join(b.Runtime, "share.sock") }

func (b *Boxes) shareForward() box.Forward {
	return box.Forward{HostPath: b.Layout.HostShareSocket(), EnginePath: b.shareSocket()}
}

// serveShare keeps dyna share serve running on this machine until ctx ends. Every other host reaches it over the connection
// the engine holds. It logs a failure only when it changes.
func (b *Boxes) serveShare(ctx context.Context, executable string) {
	last := ""
	for {
		problem := ""
		if err := podman.EnsureShareServer(ctx, executable, b.Layout.Rooms(), b.shareSocket()); err != nil && ctx.Err() == nil {
			problem = err.Error()
		}
		if problem != last && problem != "" {
			log.Printf("share server: %s", problem)
		}
		last = problem
		select {
		case <-ctx.Done():
			return
		case <-time.After(shareServerEvery):
		}
	}
}

// onEngineHost reports whether a host is the machine the engine runs on, asking it only while the store does not know.
func (b *Boxes) onEngineHost(ctx context.Context, name contract.HostName) (bool, error) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return false, err
	}
	for _, record := range hosts {
		if record.Name == name && record.EngineHost != nil {
			return *record.EngineHost, nil
		}
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return false, err
	}
	return b.engineHost(ctx, h)
}

// shareReady brings up what a host's boxes need to reach the shared folders, and reports whether it is the engine host, which
// needs nothing. An error means no box may start there: without its mount, a box would write where no other host can see.
func (b *Boxes) shareReady(ctx context.Context, name contract.HostName) (bool, error) {
	engineHost, err := b.onEngineHost(ctx, name)
	if err != nil || engineHost {
		return engineHost, err
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return false, err
	}
	if err := h.AddForward(ctx, b.shareForward()); err != nil {
		return false, err
	}
	if err := b.installHost(ctx, name); err != nil {
		return false, err
	}
	err = h.EnsureShare(ctx, b.Layout)
	var down box.ShareDown
	if errors.As(err, &down) {
		down.Host = string(name)
		return false, down
	}
	return false, err
}

// sharedMount is the /box/shared of a box in this room on this host, once the host has what it needs.
func (b *Boxes) sharedMount(ctx context.Context, name contract.HostName, room contract.RoomID) (box.Mount, error) {
	engineHost, err := b.shareReady(ctx, name)
	if err != nil {
		return box.Mount{}, err
	}
	if engineHost {
		return box.Mount{HostPath: b.Layout.Shared(room), BoxPath: "/box/shared"}, nil
	}
	return box.Mount{HostPath: b.Layout.HostShared(room), BoxPath: "/box/shared", OnMount: b.Layout.HostShareMount()}, nil
}

// repairShare brings back a mount the heartbeat found down, as after a reboot. A box already bound to a mount that died stays
// without /box/shared until it restarts. It logs a failure only when it changes, as a host can lack FUSE for days.
func (b *Boxes) repairShare(ctx context.Context, name contract.HostName) {
	_, err := b.shareReady(ctx, name)
	problem := ""
	if err != nil {
		problem = err.Error()
	}
	b.mu.Lock()
	changed := b.shareProblems[name] != problem
	if b.shareProblems == nil {
		b.shareProblems = make(map[contract.HostName]string)
	}
	b.shareProblems[name] = problem
	b.mu.Unlock()
	if changed && problem != "" {
		log.Printf("host %s: shared folder: %s", name, problem)
	}
}

// measureShared records each room's shared folder when the engine starts and every day after. Rootless bind mounts have no
// quota, so this is what warns of a folder that grows.
func (b *Boxes) measureShared(ctx context.Context) {
	for {
		sizes, err := sharedSizes(ctx, b.Layout)
		if err == nil {
			err = b.Store.SetRoomShared(ctx, sizes, time.Now())
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("measure shared folders: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(measureSharedEvery):
		}
	}
}

func sharedSizes(ctx context.Context, layout box.Layout) ([]contract.RoomShared, error) {
	rooms, err := os.ReadDir(layout.Rooms())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sizes := []contract.RoomShared{}
	for _, entry := range rooms {
		room := contract.RoomID(entry.Name())
		if _, err := os.Stat(layout.Shared(room)); err != nil {
			continue
		}
		bytes, err := folderSize(ctx, layout.Shared(room))
		if err != nil {
			return nil, err
		}
		sizes = append(sizes, contract.RoomShared{Room: room, Bytes: bytes})
	}
	return sizes, nil
}

// folderSize adds up the files it can read. A box can leave a folder under an ID this user cannot enter.
func folderSize(ctx context.Context, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
