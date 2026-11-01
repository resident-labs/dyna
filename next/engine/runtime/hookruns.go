package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/store"
)

// StartRun finds a Run's image, which can mean a build, and boots its box.
func (b *Boxes) StartRun(ctx context.Context, spec box.RunSpec) error {
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return err
	}
	image, err := b.runImage(ctx, h, spec)
	if err != nil {
		return err
	}
	return b.boot(ctx, h, spec, image)
}

// boot gives a Run its socket, its files and its box. A failure leaves nothing behind.
func (b *Boxes) boot(ctx context.Context, h *podman.Host, spec box.RunSpec, image string) (err error) {
	if err := b.Sockets.AddRun(spec.ID, spec.Subscription, spec.Room); err != nil {
		return err
	}
	forward := b.forward(spec.ID)
	defer func() {
		if err != nil {
			_ = h.RemoveRun(context.WithoutCancel(ctx), spec.ID, b.Layout.HostRun(spec.ID))
			_ = h.RemoveForward(context.WithoutCancel(ctx), forward)
			_ = b.Sockets.RemoveBox(spec.ID)
		}
	}()
	if err = h.AddForward(ctx, forward); err != nil {
		return err
	}
	folder := b.Layout.HostRun(spec.ID)
	if err = h.PutRunFiles(ctx, folder, spec.Files); err != nil {
		return err
	}
	mounts := []box.Mount{
		{HostPath: b.Layout.HostSocketFolder(spec.ID), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: folder, BoxPath: podman.RunFolder, ReadOnly: true},
	}
	if spec.Room != "" {
		mounts = append(mounts, box.Mount{HostPath: b.Layout.HostShared(spec.Room), BoxPath: "/box/shared"})
	}
	return h.StartRun(ctx, box.RunContainer{ID: string(spec.ID), Image: image, Size: spec.Size, Command: spec.Command, Mounts: mounts})
}

// runImage resolves a Run's image the way a session's is: base, or a folder of the room's default repository.
func (b *Boxes) runImage(ctx context.Context, h *podman.Host, spec box.RunSpec) (string, error) {
	base, err := h.BuildBase(ctx, images.BaseContainerfile, b.PiTarball)
	if err != nil || spec.Image == box.BaseImage {
		return base, err
	}
	repos, err := b.Store.RoomRepos(ctx, spec.Room)
	if err != nil {
		return "", err
	}
	index := slices.IndexFunc(repos, func(r contract.RoomRepo) bool { return r.Default })
	if index < 0 {
		return "", fmt.Errorf("image %q needs a default repository in its room", spec.Image)
	}
	repo := repos[index].Repo
	name, tree, data, _, err := b.Mirror.ImageContext(ctx, repo, spec.Image)
	if err != nil {
		return "", err
	}
	// A schedule starts this often, and each real build records an event, so a built image is only looked up.
	tag := engineimages.Tag(name, base, tree)
	if found, err := h.HasImage(ctx, tag); found || err != nil {
		return tag, err
	}
	return b.Images.Build(ctx, h, spec.Room, spec.Host, repo, name, base, tree, data)
}

// RestoreRun reopens the socket of a Run that outlived the last engine.
func (b *Boxes) RestoreRun(ctx context.Context, spec box.RunSpec) error {
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return err
	}
	if err := b.Sockets.AddRun(spec.ID, spec.Subscription, spec.Room); err != nil {
		return err
	}
	return h.AddForward(ctx, b.forward(spec.ID))
}

func (b *Boxes) RunStatus(ctx context.Context, host contract.HostName, id contract.BoxID) (box.RunStatus, error) {
	h, err := b.host(ctx, host)
	if err != nil {
		return box.RunStatus{}, err
	}
	return h.RunStatus(ctx, id)
}

func (b *Boxes) RunOutput(ctx context.Context, host contract.HostName, id contract.BoxID) ([]byte, error) {
	h, err := b.host(ctx, host)
	if err != nil {
		return nil, err
	}
	return h.RunOutput(ctx, id)
}

// RemoveRun clears everything of a Run. Its socket goes last, so a failure can be retried in full.
func (b *Boxes) RemoveRun(ctx context.Context, host contract.HostName, id contract.BoxID) error {
	h, err := b.host(ctx, host)
	if err != nil {
		return errors.Join(err, b.Sockets.RemoveBox(id))
	}
	return errors.Join(h.RemoveRun(ctx, id, b.Layout.HostRun(id)), h.RemoveForward(ctx, b.forward(id)), b.Sockets.RemoveBox(id))
}

// sweepRuns removes Run containers no row answers for, such as one left on a host that was lost during its Run.
func (b *Boxes) sweepRuns(ctx context.Context, name contract.HostName, h *podman.Host) {
	found, err := h.ListRuns(ctx)
	if err != nil || len(found) == 0 {
		return
	}
	active, err := b.Store.ActiveRuns(ctx)
	if err != nil {
		log.Printf("host %s: sweep runs: %v", name, err)
		return
	}
	for _, id := range found {
		if slices.ContainsFunc(active, func(run store.HookRun) bool { return run.ID == id }) {
			continue
		}
		if err := b.RemoveRun(ctx, name, id); err != nil {
			log.Printf("host %s: sweep run %s: %v", name, id, err)
		}
	}
}
