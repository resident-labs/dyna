// Package images computes reproducible toolchain image tags.
package images

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

type TreeID string

// Tag hashes the base tag and the Git tree that covers the context files.
func Tag(name, base string, tree TreeID) string {
	h := sha256.New()
	for _, value := range []string{base, string(tree)} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		h.Write(size[:])
		h.Write([]byte(value))
	}
	return fmt.Sprintf("dyna-img-%s:%x", name, h.Sum(nil))
}
