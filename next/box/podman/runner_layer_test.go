package podman

import (
	"context"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A room's ci-runner file adds layers on the shared image: Dyna names the entrypoint and the user on every start, so a layer that sets its own
// ENTRYPOINT or USER changes neither.
func TestRunnerArgsNameTheEntrypointAndUserBeforeTheImage(t *testing.T) {
	spec := testSpec()
	spec.Image = "localhost/dyna-ci-room-room_1:abc-def"
	args := runnerArgs(spec)
	image := len(args) - 1
	if args[image] != spec.Image {
		t.Fatalf("image is not last: %v", args)
	}
	joined := strings.Join(args[:image], " ")
	for _, want := range []string{`--entrypoint ["/usr/bin/tini","--","/opt/dyna-ci/entrypoint.sh"]`, "--user 1001:1001"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args miss %q: %s", want, joined)
		}
	}
}

func TestARoomLayerBuildsOnTheSharedRunnerImage(t *testing.T) {
	r := &imageRunner{missing: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := h.BuildRunnerImage(context.Background(), &box.RunnerLayer{Room: "room_1", ID: "0123456789abcdef", Context: []byte("tar")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "localhost/dyna-ci-room-room_1:0123456789abcdef-") {
		t.Fatalf("tag %s", tag)
	}
	var shared, layered string
	for _, c := range r.commands {
		if !strings.Contains(c, "'podman' 'build'") {
			continue
		}
		if strings.Contains(c, "DYNA_CI_BASE=localhost/dyna-ci-runner:") && strings.Contains(c, tag) {
			layered = c
		} else if strings.Contains(c, "localhost/dyna-ci-runner:") {
			shared = c
		}
	}
	if shared == "" || layered == "" || strings.Contains(layered, "DYNA_BASE=") {
		t.Fatalf("commands %v", r.commands)
	}
}

func TestARunnerLayerWithAnUnsafeNameIsRefused(t *testing.T) {
	h, err := New("user@host", t.TempDir(), "host", &imageRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range []*box.RunnerLayer{{Room: "a b", ID: "0123456789abcdef"}, {Room: "room_1", ID: "x;rm"}} {
		if _, err := h.BuildRunnerImage(context.Background(), layer); err == nil {
			t.Errorf("%+v built", layer)
		}
	}
}

// A room that left CI or dropped its ci-runner image has no build to replace its layer, so the prune removes it. The shared
// image and the layers of the rooms named stay.
func TestPruneRunnerLayersRemovesTheLayersOfOtherRooms(t *testing.T) {
	r := &pruneRunner{images: "localhost/dyna-ci-runner:h\nlocalhost/dyna-ci-room-room_1:a-b\nlocalhost/dyna-ci-room-gone:c-d\nlocalhost/dyna-base:x\n"}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PruneRunnerLayers(context.Background(), []contract.RoomID{"room_1"}); err != nil {
		t.Fatal(err)
	}
	if len(r.removed) != 1 || r.removed[0] != "localhost/dyna-ci-room-gone:c-d" {
		t.Fatalf("removed %v", r.removed)
	}
}
