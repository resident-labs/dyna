// Package images computes reproducible toolchain image tags.
package images

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

type entry struct {
	name, link string
	mode       int64
	kind       byte
	sum        [32]byte
}

// Tag hashes the base tag and the sorted context, including file modes and links.
func Tag(name, base string, data []byte) (string, error) {
	if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
		return "", errors.New("invalid image name")
	}
	reader := tar.NewReader(bytes.NewReader(data))
	var files []entry
	seen := make(map[string]bool)
	containerfile := false
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeSymlink {
			return "", errors.New("unsupported image context entry")
		}
		if h.Name == "." || strings.HasPrefix(h.Name, "/") || strings.HasPrefix(h.Name, "../") || path.Clean(h.Name) != h.Name || seen[h.Name] {
			return "", errors.New("invalid image context path")
		}
		seen[h.Name] = true
		if h.Name == "Containerfile" && h.Typeflag != tar.TypeSymlink {
			containerfile = true
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, reader); err != nil {
			return "", err
		}
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		files = append(files, entry{name: h.Name, link: h.Linkname, mode: h.Mode, kind: h.Typeflag, sum: sum})
	}
	if !containerfile {
		return "", errors.New("image context has no Containerfile")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	hash := sha256.New()
	write := func(data []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		hash.Write(size[:])
		hash.Write(data)
	}
	write([]byte(base))
	for _, file := range files {
		write([]byte(file.name))
		write([]byte(file.link))
		var mode [8]byte
		binary.BigEndian.PutUint64(mode[:], uint64(file.mode))
		write(mode[:])
		write([]byte{file.kind})
		write(file.sum[:])
	}
	return fmt.Sprintf("dyna-img-%s:%x", name, hash.Sum(nil)), nil
}
