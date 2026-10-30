package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
)

func TestRemoveHostBoxCleansPartialCreation(t *testing.T) {
	for _, stage := range []string{"before slice", "after slice", "after forward", "after container"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			host := fakehost.New()
			record := box.Box{ID: "box_1", Slice: "dyna-test.slice"}
			forward := box.Forward{Box: record.ID, HostPath: "/host/sock/box_1/engine.sock", EnginePath: "/runtime/boxes/box_1.sock"}
			if stage != "before slice" {
				if err := host.EnsureSlice(ctx, box.Slice{Name: record.Slice}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after forward" || stage == "after container" {
				if err := host.AddForward(ctx, forward); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after container" {
				if err := host.CreateContainer(ctx, box.ContainerSpec{Box: record.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeHostBox(ctx, host, record, forward); err != nil {
				t.Fatal(err)
			}
			if len(host.Slices) != 0 || len(host.Forwards) != 0 || len(host.Containers) != 0 || len(host.Volumes) != 0 {
				t.Fatalf("resources remain: %+v", host)
			}
		})
	}
}
