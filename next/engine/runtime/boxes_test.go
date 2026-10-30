package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/box/podman"
)

type missingContainerRunner struct{ stopSeen bool }

func (r *missingContainerRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	status := 0
	if strings.Contains(command, "'podman' '--cgroup-manager=systemd' 'stop'") {
		r.stopSeen = true
		if !strings.Contains(command, "'--ignore'") {
			status = 125
		}
	}
	if strings.Contains(command, "'volume' 'rm'") {
		status = 1
	}
	return []byte(fmt.Sprintf("DYNA-END %020d %03d\n", 0, status)), nil
}

func TestRemoveHostBoxWithMissingContainer(t *testing.T) {
	runner := &missingContainerRunner{}
	host, err := podman.New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	record := box.Box{ID: "box_1", Slice: "dyna-test.slice"}
	forward := box.Forward{Box: record.ID, HostPath: "/host/sock/box_1/engine.sock", EnginePath: "/runtime/boxes/box_1.sock"}
	if err := removeHostBox(context.Background(), host, record, forward); err != nil {
		t.Fatal(err)
	}
	if !runner.stopSeen {
		t.Fatal("cleanup did not stop the container")
	}
}

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
