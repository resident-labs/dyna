package podman

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Runners on one host share its cache, read-only, and each writes only the build folder of its own slot. A slot holds one
// running runner, so two jobs never share a build folder at once, whatever their sizes or repositories.
func TestConcurrentRunnersGetDistinctBuildFolders(t *testing.T) {
	small, _ := box.Size(contract.BoxSizeSmall)
	first, second := testSpec(), testSpec()
	first.Slot, second.Slot = 0, 1
	first.Size, second.Name, second.Repo = small, "dyna-ci-pve-2", "owner/other"
	var writable []string
	for _, spec := range []box.RunnerSpec{first, second} {
		args := runnerArgs(spec)
		var mounts []string
		for i, arg := range args {
			if arg == "--mount" && strings.HasPrefix(args[i+1], "type=bind,") {
				mounts = append(mounts, args[i+1])
			}
		}
		if len(mounts) != 2 || mounts[0] != "type=bind,source=/data/cache,destination=/cache,ro" {
			t.Fatalf("slot %d mounts %v", spec.Slot, mounts)
		}
		writable = append(writable, mounts[1])
		_, folder, _ := strings.Cut(mounts[1], ",destination=")
		if !slices.Contains(args, "DYNA_CI_BUILD="+folder) {
			t.Fatalf("DYNA_CI_BUILD does not name the writable folder %s: %v", mounts[1], args)
		}
	}
	want := []string{"type=bind,source=/data/cache/ci/0,destination=/cache/ci/0", "type=bind,source=/data/cache/ci/1,destination=/cache/ci/1"}
	if !slices.Equal(writable, want) {
		t.Fatalf("writable mounts %v, want %v", writable, want)
	}
}

func TestStartRunnerPreparesItsBuildFolderFirst(t *testing.T) {
	h, runner := newFrameHost(t)
	if err := h.StartRunner(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	prepared, started := -1, -1
	for i, args := range runner.args {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "podman unshare chown 1001:1001") && strings.Contains(joined, "/data/cache/ci/3") && strings.Contains(joined, "41943040") {
			prepared = i
		}
		if strings.Contains(joined, "dyna-runner") {
			started = i
		}
	}
	if prepared < 0 || started < prepared {
		t.Fatalf("build folder prepared at call %d, runner started at %d", prepared, started)
	}
}
