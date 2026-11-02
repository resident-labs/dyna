package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
)

// A box closed before it was created has no slice, and its cleanup still finishes.
func TestRemoveHostBoxWithoutASlice(t *testing.T) {
	runner := &missingContainerRunner{}
	host, err := podman.New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	record := box.Box{ID: "box_1"}
	forward := box.Forward{Box: record.ID, HostPath: "/host/sock/box_1/engine.sock", EnginePath: "/runtime/boxes/box_1.sock"}
	if err := removeHostBox(context.Background(), host, record, forward); err != nil {
		t.Fatalf("cleanup of a box with no slice failed: %v", err)
	}
}
