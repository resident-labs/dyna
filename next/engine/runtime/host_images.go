package runtime

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
)

func (b *Boxes) MaintainHost(ctx context.Context, name contract.HostName) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	b.prepareImages(ctx, h, name)
	b.sweepRuns(ctx, name, h)
	wanted, err := b.binarySum()
	if err != nil {
		return err
	}
	return h.Maintain(ctx, b.Layout, wanted)
}

// BuildAhead prepares every ready host's images when the engine starts, which is when a deploy changes dyna-base.
// Hosts go side by side, and each builds one image at a time, so running boxes keep the rest of their host.
func (b *Boxes) BuildAhead(ctx context.Context) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		log.Printf("build ahead: list hosts: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, record := range hosts {
		if record.State != box.HostReady || record.ExecOnly {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := b.host(ctx, record.Name)
			if err != nil {
				log.Printf("host %s: build ahead: %v", record.Name, err)
				return
			}
			b.prepareImages(ctx, h, record.Name)
		}()
	}
	wg.Wait()
}

// prepareImages builds the images that boxes on this host would use now, then prunes older ones. A failure is only
// logged: the next box that needs the image builds it and reports the failure in its room.
func (b *Boxes) prepareImages(ctx context.Context, h *podman.Host, name contract.HostName) {
	// Running boxes come first: until a host is back, image work would only compete with them for its link.
	if !b.waitRecovered(ctx, name) {
		return
	}
	steps := box.NewStepLog("host " + string(name))
	rooms, err := b.hostRepos(ctx, name)
	if err != nil {
		log.Printf("host %s: build ahead: %v", name, err)
		return
	}
	image, err := b.image(ctx, h, name, "", "", "", contract.BoxSize{})
	if err != nil {
		log.Printf("host %s: build ahead: %v", name, err)
		return
	}
	keep := []string{image.Tag}
	for _, pair := range rooms {
		image, err := b.image(ctx, h, name, pair.room, pair.repo, "", contract.BoxSize{})
		if err != nil {
			log.Printf("host %s: build ahead %s %s: %v", name, pair.room, pair.repo, err)
			continue
		}
		keep = append(keep, image.Tag)
	}
	keep = append(keep, b.prepareCompose(ctx, h, name, rooms)...)
	// A service's running image and the one before it stay: a rollback needs the previous one, and box images share the families
	// that pruning thins to the newest two.
	kept, err := b.serviceImages(ctx, name)
	if err != nil {
		log.Printf("host %s: build ahead: services: %v", name, err)
		return
	}
	keep = append(keep, kept...)
	steps.Done("build ahead")
	if err := h.PruneImages(ctx, keep); err != nil {
		log.Printf("host %s: prune images: %v", name, err)
	}
	steps.Done("prune images")
}

// serviceImages lists the image references the services on a host run or can roll back to.
func (b *Boxes) serviceImages(ctx context.Context, host contract.HostName) ([]string, error) {
	services, err := b.Store.Services(ctx, "")
	var refs []string
	for _, row := range services {
		if row.Host == host {
			refs = append(refs, row.ImageRef, row.Previous)
		}
	}
	return refs, err
}

// prepareCompose builds and pulls what the compose files in reach of this host's rooms name, and returns the tags. A file
// that fails is only logged, as an image is.
func (b *Boxes) prepareCompose(ctx context.Context, h *podman.Host, name contract.HostName, rooms []roomRepo) []string {
	if b.Compose == nil {
		return nil
	}
	var keep []string
	for _, pair := range rooms {
		names, err := b.Compose.Names(ctx, pair.room, pair.repo)
		if err != nil {
			log.Printf("host %s: build ahead compose in %s: %v", name, pair.room, err)
			continue
		}
		for _, file := range names {
			image, err := b.image(ctx, h, name, pair.room, pair.repo, file, contract.BoxSize{})
			if err != nil {
				log.Printf("host %s: build ahead compose %s in %s: %v", name, file, pair.room, err)
				continue
			}
			keep = append(keep, image.Tags()...)
		}
	}
	return keep
}

// roomRepo is one place a session can start: a room, with one of its repositories or with none.
type roomRepo struct {
	room contract.RoomID
	repo contract.RepoName
}

// hostRepos returns every room and repository pair a room on this host can use, and each room once with no repository,
// because images and compose files are the room's.
func (b *Boxes) hostRepos(ctx context.Context, name contract.HostName) ([]roomRepo, error) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	var host box.Host
	for _, record := range hosts {
		if record.Name == name {
			host = record
		}
	}
	rooms, err := b.Store.OpenRooms(ctx)
	if err != nil {
		return nil, err
	}
	var pairs []roomRepo
	for _, room := range rooms {
		// A host with agents off serves no room, so it builds no room's images.
		if !host.Serves(room) {
			continue
		}
		listed, err := b.Store.RoomRepos(ctx, room)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, roomRepo{room: room})
		for _, repo := range listed {
			pairs = append(pairs, roomRepo{room, repo.Repo})
		}
	}
	return pairs, nil
}

func (b *Boxes) CurrentImage(ctx context.Context, sessionID contract.SessionID, host contract.HostName) (box.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, box.StartPreparationBudget)
	defer cancel()
	h, err := b.host(ctx, host)
	if err != nil {
		return box.Image{}, err
	}
	session, err := b.Store.Session(ctx, sessionID)
	if err != nil {
		return box.Image{}, err
	}
	var size contract.BoxSize
	if session.Compose != "" {
		record, err := b.Store.Box(ctx, session.Box)
		if err != nil {
			return box.Image{}, err
		}
		size = record.Size
	}
	image, err := b.image(ctx, h, host, session.Room, session.Repo, session.Compose, size)
	pinned, pinErr := b.Store.PinWorkflowImage(ctx, sessionID, image.Tag)
	if pinErr != nil {
		return box.Image{}, pinErr
	}
	// A crew keeps the image it started on, even when the repository's own no longer builds. A host without that tag can only
	// build what the repository holds now.
	if pinned != "" && pinned != image.Tag {
		if has, hasErr := h.HasImage(ctx, pinned); hasErr == nil && has {
			image.Tag = pinned
			return image, nil
		}
	}
	return image, err
}

// image selects and builds what a box uses. Every box takes its image from here, whether new, swapped or built ahead. A
// compose name brings its file, which chooses the image and adds the sidecars; size checks the file's limits, and the zero
// size of a build ahead skips that.
func (b *Boxes) image(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, repo contract.RepoName, composeName string, size contract.BoxSize) (box.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, box.StartPreparationBudget)
	defer cancel()
	steps := box.NewStepLog("host " + string(host))
	base, err := h.BuildBase(ctx, images.BaseContainerfile)
	if err != nil {
		return box.Image{}, err
	}
	steps.Done("base image")
	// A room's repository images move into the room before its image is chosen, so a late mirror loses nothing.
	if room != "" && b.Migrator != nil {
		b.Migrator.Room(ctx, room, true)
	}
	var file *compose.File
	folder := ""
	if composeName != "" {
		if file, err = b.Compose.Load(ctx, room, repo, composeName); err != nil {
			return box.Image{}, err
		}
		if size.Name != "" {
			if err := file.CheckLimits(size); err != nil {
				return box.Image{}, err
			}
		}
		folder = file.Agent.Build
		steps.Done("compose file")
	}
	tag, needs, err := b.toolchain(ctx, h, host, room, folder, base)
	if err != nil {
		return box.Image{}, err
	}
	image := box.Image{Tag: tag, Needs: needs}
	if file != nil {
		if image.Stack, err = b.stack(ctx, h, host, room, repo, base, file); err != nil {
			return box.Image{}, err
		}
		steps.Done("sidecar images")
	}
	return image, nil
}

// toolchain builds one image of the room: the one called name, or its sole box image when name is empty. With neither it is base.
func (b *Boxes) toolchain(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, name, base string) (string, []box.Need, error) {
	if room == "" {
		return base, nil, nil
	}
	steps := box.NewStepLog("host " + string(host))
	name, tree, data, needs, err := b.RoomImages.Context(room, name)
	if err != nil {
		return "", nil, err
	}
	steps.Done("image context")
	if name == "" {
		return base, needs, nil
	}
	tag, err := b.Images.Build(ctx, h, room, host, "", name, base, tree, data)
	if err != nil {
		return "", nil, err
	}
	steps.Done("toolchain image")
	return tag, needs, nil
}

// stack gets each sidecar's image onto the host. box-needs grants devices, which a sidecar never gets.
func (b *Boxes) stack(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, repo contract.RepoName, base string, file *compose.File) (*box.Stack, error) {
	tags := make(map[string]string, len(file.Sidecars))
	for _, sidecar := range file.Sidecars {
		if sidecar.Image != "" {
			if err := h.PullImage(ctx, sidecar.Image); err != nil {
				return nil, err
			}
			tags[sidecar.Name] = sidecar.Image
			continue
		}
		tag, needs, err := b.toolchain(ctx, h, host, room, sidecar.Build, base)
		if err != nil {
			return nil, err
		}
		if len(needs) > 0 {
			return nil, box.StackError{Reason: fmt.Sprintf("service %s: image %s has box-needs, which is only for the agent", sidecar.Name, sidecar.Build)}
		}
		tags[sidecar.Name] = tag
	}
	return file.Stack(base, tags), nil
}
