package images

import "testing"

func TestTagDependsOnBaseAndTree(t *testing.T) {
	tag := Tag("toolchain", "dyna-base:one", "tree-a")
	if tag != Tag("toolchain", "dyna-base:one", "tree-a") || tag == Tag("toolchain", "dyna-base:two", "tree-a") || tag == Tag("toolchain", "dyna-base:one", "tree-b") {
		t.Fatal("image tag does not identify its base and tree")
	}
}
