// Package runtime connects the engine to host transports.
package runtime

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/hostexec"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/store"
)

type boxStore interface {
	Hosts(context.Context) ([]box.Host, error)
	AddHost(context.Context, box.Host) error
	Session(context.Context, contract.SessionID) (store.Session, error)
	Box(context.Context, contract.BoxID) (box.Box, error)
	Boxes(context.Context) ([]box.Box, error)
}

type Boxes struct {
	Store     boxStore
	Sockets   *api.Sockets
	Runtime   string
	HostData  string
	Version   string
	PiTarball []byte
	Mirror    *enginegit.Mirror
	Images    *engineimages.Builder
	Context   context.Context
	Pressure  *engine.Service
	mu        sync.Mutex
	hosts     map[contract.HostName]*podman.Host
}

// cache stores a host driver and starts its heartbeat until the engine stops. A cached name keeps its first driver and heartbeat. The caller holds b.mu.
func (b *Boxes) cache(name contract.HostName, h *podman.Host) {
	if b.hosts == nil {
		b.hosts = make(map[contract.HostName]*podman.Host)
	}
	if b.hosts[name] != nil {
		return
	}
	b.hosts[name] = h
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		defer ticker.Stop()
		h.Heartbeat(b.Context, ticker.C, func(ok bool) { log.Printf("host %s reachable: %t", name, ok) }, b.pressure(name))
	}()
}

func (b *Boxes) host(ctx context.Context, name contract.HostName) (*podman.Host, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h := b.hosts[name]; h != nil {
		return h, nil
	}
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range hosts {
		if record.Name == name {
			h, err := podman.New(record.SSH, b.Runtime, string(name), nil)
			if err != nil {
				return nil, err
			}
			b.cache(name, h)
			return h, nil
		}
	}
	return nil, fmt.Errorf("host %q not found", name)
}

func (b *Boxes) CacheHost(ctx context.Context, name contract.HostName) (enginegit.CacheHost, string, error) {
	h, err := b.host(ctx, name)
	return h, b.HostData, err
}

func (b *Boxes) AddHost(ctx context.Context, req contract.HostAddRequest) error {
	h, err := podman.New(req.SSH, b.Runtime, string(req.Name), nil)
	if err != nil {
		return err
	}
	check := h.Check(ctx, podman.CheckOptions{HostData: b.HostData, Runtime: b.Runtime, AdminOnly: req.OwnerOnlyAdmin})
	if !check.Ready() {
		for _, item := range check.Items {
			if !item.OK {
				return fmt.Errorf("host check %s: %s", item.Name, item.Reason)
			}
		}
		return fmt.Errorf("host check failed")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	if err := h.InstallBinary(ctx, b.HostData, b.Version, binary); err != nil {
		return err
	}
	record := box.Host{Name: req.Name, SSH: req.SSH, State: box.HostReady, CPUs: check.CPUs, MemBytes: check.MemBytes, DiskBytes: check.DiskBytes, CheckedAt: time.Now()}
	if err := b.Store.AddHost(ctx, record); err != nil {
		return err
	}
	b.mu.Lock()
	b.cache(req.Name, h)
	b.mu.Unlock()
	return nil
}

func (b *Boxes) Create(ctx context.Context, spec engine.BoxSpec) (string, error) {
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return "", err
	}
	session, err := b.Store.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	if err := b.Sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", err
	}
	ok := false
	var forwarded, sliced, created bool
	forward := box.Forward{Box: spec.Box, HostPath: filepath.Join(b.HostData, "sock", string(spec.Box), "engine.sock"), EnginePath: filepath.Join(b.Runtime, "boxes", string(spec.Box)+".sock")}
	defer func() {
		if ok {
			return
		}
		if created {
			_ = h.RemoveContainer(context.Background(), spec.Box)
			_ = h.RemoveVolume(context.Background(), spec.Box)
		}
		if forwarded {
			_ = h.RemoveForward(context.Background(), forward)
		}
		if sliced {
			_ = h.RemoveSlice(context.Background(), spec.Slice)
		}
		_ = b.Sockets.RemoveBox(spec.Box)
	}()
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return "", err
	}
	if err = h.InstallBinary(ctx, b.HostData, b.Version, binary); err != nil {
		return "", err
	}
	image, err := h.BuildBase(ctx, images.BaseContainerfile, b.PiTarball)
	if err != nil {
		return "", err
	}
	if session.Repo != "" {
		name, tree, data, err := b.Mirror.ImageContext(ctx, session.Repo)
		if err != nil {
			return "", err
		}
		if name != "" {
			image, err = b.Images.Build(ctx, h, session.Room, spec.Host, session.Repo, name, image, tree, data)
			if err != nil {
				return "", err
			}
		}
	}
	slice, c := b.container(spec, image)
	if err = h.EnsureSlice(ctx, slice); err != nil {
		return "", err
	}
	sliced = true
	if err = h.AddForward(ctx, forward); err != nil {
		return "", err
	}
	forwarded = true
	if err = h.CreateContainer(ctx, c); err != nil {
		return "", err
	}
	created = true
	if err = h.StartContainer(ctx, spec.Box); err != nil {
		return "", err
	}
	ok = true
	return image, nil
}

// Restore reconnects live allocations without starting their containers.
func (b *Boxes) Restore(ctx context.Context) error {
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	installed := make(map[contract.HostName]bool)
	for _, record := range records {
		if record.Host == "" || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		session, err := b.Store.Session(ctx, record.Session)
		if err != nil {
			return err
		}
		if err := b.Sockets.AddBox(record.ID, record.Session, session.Room); err != nil {
			return err
		}
		host, err := b.host(ctx, record.Host)
		if err != nil {
			return err
		}
		if !installed[record.Host] {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			binary, err := os.ReadFile(executable)
			if err != nil {
				return err
			}
			if err := host.InstallBinary(ctx, b.HostData, b.Version, binary); err != nil {
				return err
			}
			installed[record.Host] = true
		}
		forward := box.Forward{Box: record.ID, HostPath: filepath.Join(b.HostData, "sock", string(record.ID), "engine.sock"), EnginePath: filepath.Join(b.Runtime, "boxes", string(record.ID)+".sock")}
		if err := host.AddForward(ctx, forward); err != nil {
			return err
		}
	}
	return nil
}

func (b *Boxes) containerHost(ctx context.Context, id contract.BoxID) (*podman.Host, error) {
	record, err := b.Store.Box(ctx, id)
	if err != nil {
		return nil, err
	}
	return b.host(ctx, record.Host)
}

func (b *Boxes) Start(ctx context.Context, id contract.BoxID) error {
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	return host.StartContainer(ctx, id)
}

func (b *Boxes) Stop(ctx context.Context, id contract.BoxID) error {
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	return host.StopContainer(ctx, id)
}

func (b *Boxes) Remove(ctx context.Context, id contract.BoxID) error {
	record, err := b.Store.Box(ctx, id)
	if err != nil {
		return err
	}
	if record.Host == "" {
		return nil
	}
	h, err := b.host(ctx, record.Host)
	if err != nil {
		return err
	}
	forward := box.Forward{Box: id, HostPath: filepath.Join(b.HostData, "sock", string(id), "engine.sock"), EnginePath: filepath.Join(b.Runtime, "boxes", string(id)+".sock")}
	if err := removeHostBox(ctx, h, record, forward); err != nil {
		return err
	}
	return b.Sockets.RemoveBox(id)
}

// removeHostBox cleans up every host resource, including a partial creation.
func removeHostBox(ctx context.Context, h box.HostDriver, record box.Box, forward box.Forward) error {
	if err := h.StopContainer(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveForward(ctx, forward); err != nil {
		return err
	}
	if err := h.RemoveContainer(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveVolume(ctx, record.ID); err != nil {
		return err
	}
	return h.RemoveSlice(ctx, record.Slice)
}

type scopeRunner interface {
	RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error)
	StopHostexec(context.Context, string) error
}

func (b *Boxes) scopeRunner(ctx context.Context, host contract.HostName) (scopeRunner, error) {
	if host == contract.ControlPlane {
		return hostexec.Local{}, nil
	}
	return b.host(ctx, host)
}
func (b *Boxes) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	r, err := b.scopeRunner(ctx, a.Host)
	if err != nil {
		return nil, 0, false, err
	}
	return r.RunHostexec(ctx, a, op)
}
func (b *Boxes) StopHostexec(ctx context.Context, host contract.HostName, scope string) error {
	r, err := b.scopeRunner(ctx, host)
	if err != nil {
		return err
	}
	return r.StopHostexec(ctx, scope)
}

// RemoveContainer keeps the volume and forwarding socket for a moved box.
func (b *Boxes) RemoveContainer(ctx context.Context, id contract.BoxID) error {
	h, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	return h.RemoveContainer(ctx, id)
}

// Recreate replaces a moved container without touching its volume.
func (b *Boxes) Recreate(ctx context.Context, spec engine.BoxSpec, image string) error {
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return err
	}
	if err := h.StopContainer(ctx, spec.Box); err != nil {
		return err
	}
	if err := h.RemoveContainer(ctx, spec.Box); err != nil {
		return err
	}
	slice, c := b.container(spec, image)
	if err := h.EnsureSlice(ctx, slice); err != nil {
		return err
	}
	if err := h.CreateContainer(ctx, c); err != nil {
		return err
	}
	return h.StartContainer(ctx, spec.Box)
}

func (b *Boxes) container(spec engine.BoxSpec, image string) (box.Slice, box.ContainerSpec) {
	size := spec.Size
	slice := box.Slice{Name: spec.Slice, CPUs: size.CPUs, MemHigh: size.MemBytes, MemMax: size.MemBytes * 3 / 2, Pids: size.Pids}
	c := box.ContainerSpec{Box: spec.Box, Image: image, Slice: spec.Slice, Mounts: []box.Mount{
		{HostPath: filepath.Join(b.HostData, "sock", string(spec.Box)), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: filepath.Join(b.HostData, "bin"), BoxPath: "/opt/dyna/bin", ReadOnly: true},
	}}
	for _, repo := range spec.Repos {
		c.Mounts = append(c.Mounts, box.Mount{HostPath: repo.Cache, BoxPath: repo.Cache, ReadOnly: true})
	}
	return slice, c
}
