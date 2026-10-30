package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

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
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	_, err := h.run(ctx, "podman", "image", "exists", tag)
	if err == nil {
		return tag, nil
	}
	var exit remoteExit
	if !errors.As(err, &exit) || exit != 1 {
		return "", err
	}
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
	_, err = h.runInput(ctx, contextTar.Bytes(), "podman", "build", "--tag", tag, "-")
	if err != nil {
		return "", err
	}
	return tag, nil
}
