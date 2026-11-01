package podman

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
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

// BuildBase builds or reuses the base image from the exact supplied inputs.
// Generate piTarball with next/third_party/pi/scripts/pi-pack.sh.
func (h *Host) BuildBase(ctx context.Context, containerfile, piTarball []byte) (string, error) {
	hash := sha256.New()
	for _, input := range [][]byte{containerfile, piTarball} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(input)))
		hash.Write(size[:])
		hash.Write(input)
	}
	tag := fmt.Sprintf("dyna-base:%x", hash.Sum(nil))
	var contextTar bytes.Buffer
	tw := tar.NewWriter(&contextTar)
	for _, file := range []struct {
		name string
		data []byte
	}{{"Containerfile", containerfile}, {"pi.tgz", piTarball}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0644, Size: int64(len(file.data))}); err != nil {
			return "", err
		}
		if _, err := tw.Write(file.data); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	return tag, h.Build(ctx, tag, "", contextTar.Bytes())
}

// BuildError reports that podman build exited non-zero.
// Tail holds the last 4096 bytes of the build output.
type BuildError struct{ Tail string }

func (e *BuildError) Error() string { return "podman build failed" }

// BuildLog returns the build output tail.
func (e *BuildError) BuildLog() string { return e.Tail }

// Build builds or reuses a box image. A new image comes back ready for boxUserNS.
func (h *Host) Build(ctx context.Context, tag, base string, contextTar []byte) error {
	return h.build(ctx, tag, base, contextTar, true)
}

func (h *Host) build(ctx context.Context, tag, base string, contextTar []byte, warm bool) error {
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := h.run(ctx, "podman", "image", "exists", tag)
	if err == nil {
		return nil
	}
	var exit remoteExit
	if !errors.As(err, &exit) || exit != 1 {
		return err
	}
	args := []string{"podman", "build", "-t", tag}
	if base != "" {
		args = append(args, "--build-arg", "DYNA_BASE="+base)
	}
	args = append(args, "-")
	// A build has no bound, so like a warm it takes no short-command slot.
	out, err := h.runUnbounded(ctx, contextTar, args...)
	if errors.As(err, &exit) {
		return &BuildError{Tail: string(out[max(0, len(out)-4096):])}
	}
	if err != nil || !warm {
		return err
	}
	steps := box.NewStepLog("image " + tag)
	_, err = h.runUnbounded(ctx, nil, "bash", "-c", `timeout -k 5 "$1" podman create --replace --name "$2" "$3" "$4" true && podman rm "$2"`, "dyna-warm", strconv.Itoa(warmLimit), warmName, boxUserNS, tag)
	steps.Done("keep-id warm")
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
