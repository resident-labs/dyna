package podman

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Runners on one host share its cache, read-only, and each writes only the build folder of its own slot. A slot holds one
// running runner, so two jobs never share a build folder at once, whatever their sizes or repositories. Each sees its folder at
// one path, and Cargo's home and target in it where a box of its repository has them, so the compiler cache's keys match across
// slots and with boxes.
func TestConcurrentRunnersGetDistinctBuildFolders(t *testing.T) {
	small, _ := box.Size(contract.BoxSizeSmall)
	first, second := testSpec(), testSpec()
	first.Slot, second.Slot = 0, 1
	first.Size, second.Name, second.Repo = small, "dyna-ci-pve-2", "owner/other"
	var writable [][]string
	for _, spec := range []box.RunnerSpec{first, second} {
		args := runnerArgs(spec)
		var mounts []string
		for i, arg := range args {
			if arg == "--mount" && strings.HasPrefix(args[i+1], "type=bind,") {
				mounts = append(mounts, args[i+1])
			}
		}
		if len(mounts) != 5 || mounts[0] != "type=bind,source=/data/cache,destination=/cache,ro" || mounts[4] != "type=bind,source=/data/sock/build-cache,destination=/run/dyna-build-cache,ro" {
			t.Fatalf("slot %d mounts %v", spec.Slot, mounts)
		}
		writable = append(writable, mounts[1:4])
		_, folder, _ := strings.Cut(mounts[1], ",destination=")
		if !slices.Contains(args, "DYNA_CI_BUILD="+folder) {
			t.Fatalf("DYNA_CI_BUILD does not name the writable folder %s: %v", mounts[1], args)
		}
	}
	want := [][]string{
		{"type=bind,source=/data/cache/ci/0,destination=/build", "type=bind,source=/data/cache/ci/0/cargo,destination=/box/cache/cargo", "type=bind,source=/data/cache/ci/0/target,destination=/box/work/owner/repo/target"},
		{"type=bind,source=/data/cache/ci/1,destination=/build", "type=bind,source=/data/cache/ci/1/cargo,destination=/box/cache/cargo", "type=bind,source=/data/cache/ci/1/target,destination=/box/work/owner/other/target"},
	}
	if !reflect.DeepEqual(writable, want) {
		t.Fatalf("writable mounts %v, want %v", writable, want)
	}
}

func TestStartRunnerRefusesARepositoryNameThatLeavesItsFolder(t *testing.T) {
	h, _ := newFrameHost(t)
	for _, repo := range []contract.RepoName{"owner/..", "owner", "owner/repo,ro", "../repo"} {
		spec := testSpec()
		spec.Repo = repo
		if err := h.StartRunner(context.Background(), spec); err == nil {
			t.Errorf("%s started", repo)
		}
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
