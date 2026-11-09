package podman

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
)

// boxUserNS is every box's user namespace. Podman chowns a whole image the first time a container
// uses this mapping, and holds its store lock while it does, so Build does that once ahead of time.
const boxUserNS = "--userns=keep-id:uid=1000,gid=1000"

// warmName names the throwaway container that makes Podman do that. Builds are serial, so one name serves.
const warmName = "dyna-warm"

// warmLimit bounds that lock in seconds. Every Podman command on the host waits for it, though running boxes do not.
// An image that misses the limit still works, and its first box pays, as it would with no warm at all.
const warmLimit = 120

// imagesKept is how many tags of one image stay on a host.
const imagesKept = 2

// BuildBase builds or reuses the base image from the exact Containerfile. Its hash is the tag.
func (h *Host) BuildBase(ctx context.Context, containerfile []byte) (string, error) {
	tag := fmt.Sprintf("dyna-base:%x", sha256.Sum256(containerfile))
	var contextTar bytes.Buffer
	tw := tar.NewWriter(&contextTar)
	if err := tw.WriteHeader(&tar.Header{Name: "Containerfile", Mode: 0644, Size: int64(len(containerfile))}); err != nil {
		return "", err
	}
	if _, err := tw.Write(containerfile); err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	_, err := h.Build(ctx, tag, "", contextTar.Bytes())
	return tag, err
}

// BuildError reports that podman build exited non-zero.
// Tail holds the last 4096 bytes of the build output.
type BuildError struct{ Tail string }

func (e *BuildError) Error() string { return "podman build failed" }

// BuildLog returns the build output tail.
func (e *BuildError) BuildLog() string { return e.Tail }

// Build builds or reuses a box image, and says whether it built one. A new image comes back ready for boxUserNS.
func (h *Host) Build(ctx context.Context, tag, base string, contextTar []byte) (bool, error) {
	return h.build(ctx, tag, base, contextTar, true)
}

func (h *Host) build(ctx context.Context, tag, base string, contextTar []byte, warm bool) (bool, error) {
	return h.buildWith(ctx, tag, "DYNA_BASE", base, contextTar, warm)
}

// buildWith is build with the name of the argument that carries the base.
func (h *Host) buildWith(ctx context.Context, tag, baseArg, base string, contextTar []byte, warm bool) (bool, error) {
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	have, err := h.HasImage(ctx, tag)
	if have || err != nil {
		return false, err
	}
	var exit remoteExit
	args := []string{"podman", "build", "-t", tag}
	if base != "" {
		args = append(args, "--build-arg", baseArg+"="+base)
	}
	args = append(args, "-")
	// A build has no bound, so like a warm it takes no short-command slot.
	out, err := h.runUnbounded(ctx, contextTar, args...)
	if errors.As(err, &exit) {
		return false, &BuildError{Tail: string(out[max(0, len(out)-4096):])}
	}
	if err != nil || !warm {
		return err == nil, err
	}
	return true, h.warm(ctx, tag)
}

// warm makes Podman copy an image for boxUserNS now. The caller holds buildGate.
func (h *Host) warm(ctx context.Context, tag string) error {
	steps := box.NewStepLog("image " + tag)
	_, err := h.runUnbounded(ctx, nil, "bash", "-c", `timeout -k 5 "$1" podman create --replace --name "$2" "$3" "$4" true && podman rm "$2"`, "dyna-warm", strconv.Itoa(warmLimit), warmName, boxUserNS, tag)
	steps.Done("keep-id warm")
	var exit remoteExit
	if errors.As(err, &exit) {
		log.Printf("host %s: image %s not warmed, so its first box does this: %v", h.destination, tag, err)
		return nil
	}
	return err
}

// PruneImages removes all but the newest imagesKept tags of dyna-base and of each toolchain image. It keeps the tags
// in keep whatever their age, since a host can go back to an older one. podman rmi refuses an image that a container
// uses, stopped ones too, or that another image is built on; those wait for a later pass.
func (h *Host) PruneImages(ctx context.Context, keep []string) error {
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	out, err := h.run(ctx, "podman", "images", "--format", "json")
	if err != nil {
		return err
	}
	var listed []struct {
		Names   []string
		Created int64
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return err
	}
	type tagged struct {
		tag     string
		created int64
	}
	tags := make(map[string][]tagged)
	for _, image := range listed {
		for _, name := range image.Names {
			repo, _, ok := strings.Cut(name, ":")
			if short := strings.TrimPrefix(repo, "localhost/"); ok && (short == "dyna-base" || strings.HasPrefix(short, "dyna-img-")) {
				tags[short] = append(tags[short], tagged{name, image.Created})
			}
		}
	}
	for _, family := range tags {
		slices.SortFunc(family, func(a, b tagged) int { return cmp.Compare(b.created, a.created) })
		for _, stale := range family[min(imagesKept, len(family)):] {
			if slices.ContainsFunc(keep, func(tag string) bool { return strings.TrimPrefix(stale.tag, "localhost/") == tag }) {
				continue
			}
			_, err := h.run(ctx, "podman", "rmi", stale.tag)
			var exit remoteExit
			if errors.As(err, &exit) {
				log.Printf("host %s: keeping image %s: still in use", h.destination, stale.tag)
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}
