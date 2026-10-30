// Package images computes reproducible toolchain image tags.
package images

import (
	"crypto/sha256"
	"fmt"
)

type TreeID string

// Tag hashes the base tag and the Git tree that covers the context files.
func Tag(name, base string, tree TreeID) string {
	return fmt.Sprintf("dyna-img-%s:%x", name, sha256.Sum256([]byte(base+"\x00"+string(tree))))
}
