package images

import (
	"context"
	"errors"
	"testing"
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
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree treeid\ttoolchain\n"), []byte("tar")}}
	name, tree, data, err := Context(context.Background(), r, "/mirror")
	if err != nil || name != "toolchain" || tree != "treeid" || string(data) != "tar" || r.calls[2][len(r.calls[2])-1] != "treeid" {
		t.Fatalf("name %q tree %q data %q err %v", name, tree, data, err)
	}
}
func TestContextRejectsAmbiguousImage(t *testing.T) {
	r := &fakeRunner{results: [][]byte{[]byte("040000 tree root\t.dyna/images\n"), []byte("040000 tree first\ta\n040000 tree second\tb\n")}}
	_, _, _, err := Context(context.Background(), r, "/mirror")
	if !errors.Is(err, ErrImageChoice) {
		t.Fatalf("error %v", err)
	}
}
