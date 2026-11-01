package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

type imageRunner struct {
	missing  bool
	warmExit int
	commands []string
	input    []byte
}

func (r *imageRunner) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	if strings.Contains(command, "'image' 'exists'") && r.missing {
		return []byte("DYNA-END 00000000000000000000 001\n"), nil
	}
	if strings.Contains(command, "'podman' 'build'") {
		r.input = append([]byte(nil), input...)
	}
	if strings.Contains(command, "podman create --replace") && r.warmExit != 0 {
		return []byte(fmt.Sprintf("DYNA-END 00000000000000000000 %03d\n", r.warmExit)), nil
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestBuildBaseSendsTaggedContext(t *testing.T) {
	r := &imageRunner{missing: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := h.BuildBase(context.Background(), []byte("container"), []byte("pi"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "dyna-base:5e39aec50ee63e9a28cb339d41a69cef7f83a486e64807583b6f72098a0c4af1"
	if tag != want {
		t.Fatalf("tag %s", tag)
	}
	if len(r.commands) != 3 || !strings.HasSuffix(r.commands[0], "'podman' 'image' 'exists' '"+tag+"'") || !strings.HasSuffix(r.commands[1], "'podman' 'build' '-t' '"+tag+"' '-'") {
		t.Fatalf("commands %v", r.commands)
	}
	if !strings.Contains(r.commands[2], "podman create --replace") || !strings.HasSuffix(r.commands[2], "'"+boxUserNS+"' '"+tag+"'") {
		t.Fatalf("a new image was not warmed with the box mapping: %s", r.commands[2])
	}
	i := bytes.IndexByte(r.input, '\n')
	if i < 0 || string(r.input[:i+1]) != fmt.Sprintf("DYNA-IN %020d\n", len(r.input)-i-1) {
		t.Fatal("invalid input frame")
	}
	tr := tar.NewReader(bytes.NewReader(r.input[i+1:]))
	for _, file := range []struct{ name, content string }{{"Containerfile", "container"}, {"pi.tgz", "pi"}} {
		header, err := tr.Next()
		if err != nil || header.Name != file.name {
			t.Fatalf("header %v: %v", header, err)
		}
		data, err := io.ReadAll(tr)
		if err != nil || string(data) != file.content {
			t.Fatalf("content %q: %v", data, err)
		}
	}
}

func TestBuildPassesBaseTag(t *testing.T) {
	r := &imageRunner{missing: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Build(context.Background(), "toolchain:hash", "dyna-base:hash", []byte("tar")); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 3 || !strings.HasSuffix(r.commands[1], "'podman' 'build' '-t' 'toolchain:hash' '--build-arg' 'DYNA_BASE=dyna-base:hash' '-'") {
		t.Fatalf("commands %v", r.commands)
	}
	if !bytes.HasSuffix(r.input, []byte("tar")) {
		t.Fatal("missing tar input")
	}
}

type buildFailureRunner struct {
	imageRunner
	output []byte
}

func (r *buildFailureRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if !strings.Contains(args[len(args)-1], "'podman' 'build'") {
		return r.imageRunner.Run(ctx, input, args...)
	}
	return append(append([]byte(nil), r.output...), fmt.Sprintf("DYNA-END %020d 001\n", len(r.output))...), nil
}

func TestBuildFailureReturnsOutputTail(t *testing.T) {
	output := []byte(strings.Repeat("a", 5000) + strings.Repeat("b", 4096))
	r := &buildFailureRunner{imageRunner: imageRunner{missing: true}, output: output}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	err = h.Build(context.Background(), "toolchain:hash", "", []byte("tar"))
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("expected BuildError, got %v", err)
	}
	if want := strings.Repeat("b", 4096); buildErr.Tail != want || buildErr.BuildLog() != want {
		t.Fatalf("tail has %d bytes, want the last 4096", len(buildErr.Tail))
	}
}

type blockedImageRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockedImageRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	close(r.started)
	<-r.release
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestBuildBaseCancellationWhileAnotherBuildRuns(t *testing.T) {
	r := &blockedImageRunner{started: make(chan struct{}), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.BuildBase(context.Background(), []byte("container"), []byte("pi"))
		done <- err
	}()
	<-r.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.BuildBase(ctx, []byte("container"), []byte("pi"))
	close(r.release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestBuildBaseReusesExistingTag(t *testing.T) {
	r := &imageRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.BuildBase(context.Background(), []byte("container"), []byte("pi")); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 1 || !strings.Contains(r.commands[0], "'image' 'exists'") {
		t.Fatalf("commands %v", r.commands)
	}
}

func TestBoxesAndWarmsUseOneMapping(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	if err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "box_1", Image: "image", Slice: "dyna-box_1.slice"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.calls[len(r.calls)-1], " "), "'"+boxUserNS+"'") {
		t.Fatalf("box does not use the warmed mapping: %v", r.calls[len(r.calls)-1])
	}
}

// timeout exits 124 when it stops a warm. The image is whole, so a box can still use it.
func TestAWarmThatMissesItsLimitLeavesTheImageUsable(t *testing.T) {
	r := &imageRunner{missing: true, warmExit: 124}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Build(context.Background(), "toolchain:hash", "dyna-base:hash", []byte("tar")); err != nil {
		t.Fatalf("a stopped warm failed the build: %v", err)
	}
	if !strings.Contains(r.commands[2], "timeout -k 5") {
		t.Fatalf("warm has no limit: %s", r.commands[2])
	}
}

func TestReusedImageIsNotWarmedAgain(t *testing.T) {
	r := &imageRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Build(context.Background(), "toolchain:hash", "dyna-base:hash", []byte("tar")); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 1 {
		t.Fatalf("commands %v", r.commands)
	}
}

func TestRunnerImagesAreNotWarmed(t *testing.T) {
	r := &imageRunner{missing: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.build(context.Background(), "localhost/dyna-ci-runner:hash", "", []byte("tar"), false); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 2 {
		t.Fatalf("commands %v", r.commands)
	}
}

// pruneRunner answers podman images and fails podman rmi for the images named in used.
type pruneRunner struct {
	images  string
	used    map[string]bool
	removed []string
}

func (r *pruneRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		return nil, nil
	}
	command := args[len(args)-1]
	reply := func(body string, status int) ([]byte, error) {
		return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
	}
	if strings.Contains(command, "'podman' 'images'") {
		return reply(r.images, 0)
	}
	fields := strings.Fields(command)
	tag := strings.Trim(fields[len(fields)-1], "'")
	if r.used[tag] {
		return reply("image is in use by a container", 2)
	}
	r.removed = append(r.removed, tag)
	return reply("", 0)
}

func TestPruneKeepsTheNewestTwoTagsOfEachImage(t *testing.T) {
	r := &pruneRunner{images: `[
		{"Names":["localhost/dyna-base:d"],"Created":40},
		{"Names":["localhost/dyna-base:a"],"Created":10},
		{"Names":["localhost/dyna-base:c"],"Created":30},
		{"Names":["localhost/dyna-base:b"],"Created":20},
		{"Names":["localhost/dyna-img-go:b"],"Created":20},
		{"Names":["localhost/dyna-img-go:a"],"Created":10},
		{"Names":["localhost/dyna-img-node:a"],"Created":5},
		{"Names":["localhost/dyna-ci-runner:a"],"Created":1},
		{"Names":["docker.io/library/node:22"],"Created":1},
		{"Names":null,"Created":1}
	]`}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PruneImages(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	slices.Sort(r.removed)
	if !slices.Equal(r.removed, []string{"localhost/dyna-base:a", "localhost/dyna-base:b"}) {
		t.Fatalf("removed %v", r.removed)
	}
}

func TestPruneKeepsAnOldTagThatIsCurrentAgain(t *testing.T) {
	r := &pruneRunner{images: `[{"Names":["localhost/dyna-base:c"],"Created":30},{"Names":["localhost/dyna-base:b"],"Created":20},{"Names":["localhost/dyna-base:a"],"Created":10}]`}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PruneImages(context.Background(), []string{"dyna-base:a"}); err != nil {
		t.Fatal(err)
	}
	if len(r.removed) != 0 {
		t.Fatalf("removed %v", r.removed)
	}
}

func TestPruneLeavesAnImageInUseAndCarriesOn(t *testing.T) {
	r := &pruneRunner{used: map[string]bool{"localhost/dyna-base:a": true}, images: `[{"Names":["localhost/dyna-base:d"],"Created":40},{"Names":["localhost/dyna-base:c"],"Created":30},{"Names":["localhost/dyna-base:b"],"Created":20},{"Names":["localhost/dyna-base:a"],"Created":10}]`}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PruneImages(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.removed, []string{"localhost/dyna-base:b"}) {
		t.Fatalf("removed %v", r.removed)
	}
}

func TestPruneNeverForcesRemoval(t *testing.T) {
	source, err := os.ReadFile("image.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, force := range []string{`"--force"`, `"-f"`, "prune -a"} {
		if strings.Contains(string(source), force) {
			t.Fatalf("image.go uses %s, which could remove an image a stopped box needs", force)
		}
	}
}
