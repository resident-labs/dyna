package images

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/box"
)

type fakeRunner struct {
	results [][]byte
	calls   [][]string
}

func (r *fakeRunner) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}
func TestContextUsesSoleImageTree(t *testing.T) {
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree treeid\ttoolchain\n"), nil, []byte("tar")}}
	name, tree, data, _, err := Context(context.Background(), r, "/mirror", "")
	if err != nil || name != "toolchain" || tree != "treeid" || string(data) != "tar" || r.calls[3][len(r.calls[3])-1] != "treeid" {
		t.Fatalf("name %q tree %q data %q err %v", name, tree, data, err)
	}
}
func TestContextReadsNeeds(t *testing.T) {
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree treeid\ttoolchain\n"), []byte("100644 blob id\tbox-needs\n"), []byte("nested-containers\n"), []byte("tar")}}
	_, _, _, needs, err := Context(context.Background(), r, "/mirror", "")
	if err != nil || len(needs) != 1 || needs[0] != box.NeedNestedContainers {
		t.Fatalf("needs %v: %v", needs, err)
	}
}

func TestContextRejectsUnknownNeed(t *testing.T) {
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree treeid\ttoolchain\n"), []byte("100644 blob id\tbox-needs\n"), []byte("arbitrary-device")}}
	_, _, _, _, err := Context(context.Background(), r, "/mirror", "")
	if err == nil {
		t.Fatal("accepted unknown need")
	}
}

func TestContextRejectsAmbiguousImage(t *testing.T) {
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree first\ta\n040000 tree second\tb\n")}}
	_, _, _, _, err := Context(context.Background(), r, "/mirror", "")
	if !errors.Is(err, ErrImageChoice) {
		t.Fatalf("error %v", err)
	}
}

func TestContextFallsBackToImageBranch(t *testing.T) {
	r := &fakeRunner{results: [][]byte{nil, []byte("branchcommit\n"), []byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree treeid\ttoolchain\n"), nil, []byte("tar")}}
	name, tree, _, _, err := Context(context.Background(), r, "/mirror", "")
	if err != nil || name != "toolchain" || tree != "treeid" {
		t.Fatalf("name %q tree %q err %v", name, tree, err)
	}
	if got := r.calls[2][3]; got != "branchcommit" {
		t.Fatalf("listed images of %q", got)
	}
}

func TestContextWithoutImageBranchHasNoImage(t *testing.T) {
	r := &fakeRunner{results: [][]byte{nil, nil}}
	name, _, _, _, err := Context(context.Background(), r, "/mirror", "")
	if err != nil || name != "" {
		t.Fatalf("name %q err %v", name, err)
	}
}
