package localci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeScratch is a machine's scratch folder in memory. Removing a path frees
// its bytes, so the guard sees the same free space a real disk would show.
type fakeScratch struct {
	folders []ScratchFolder
	free    int64
	removed []string
}

func (f *fakeScratch) List(context.Context) ([]ScratchFolder, error) {
	return append([]ScratchFolder(nil), f.folders...), nil
}
func (f *fakeScratch) Free(context.Context) (int64, error) { return f.free, nil }
func (f *fakeScratch) Remove(_ context.Context, rel string) error {
	for i, folder := range f.folders {
		if folder.Name == rel {
			f.free += folder.Bytes
			f.folders = append(f.folders[:i], f.folders[i+1:]...)
			f.removed = append(f.removed, rel)
			return nil
		}
		for _, b := range folder.Builds {
			if folder.Name+"/"+b.Path == rel {
				f.free += b.Bytes
				f.removed = append(f.removed, rel)
				return nil
			}
		}
	}
	return errors.New("no such path " + rel)
}

// A task folder goes by its last file change, never by its name. Below the
// floor, build outputs idle for six hours go oldest first and stop once the
// machine is over the floor; sources and fresh builds stay.
func TestSweepScratchRetainsByActivityAndFreesBuildsOldestFirst(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	h := time.Hour
	host := &fakeScratch{free: 5, folders: []ScratchFolder{
		// Named like a live session, but idle for eight days: removed.
		{Name: "ses_20261001-000000-aaaaaaaa", Newest: ago(8 * 24 * h), Bytes: 10},
		// Named by task, old name, but written to an hour ago: kept whole.
		{Name: "task-live", Newest: ago(h), Bytes: 100, Builds: []ScratchBuild{{Path: "target", Newest: ago(h), Bytes: 50}}},
		{Name: "task-b", Newest: ago(3 * 24 * h), Bytes: 100, Builds: []ScratchBuild{
			{Path: "node_modules", Newest: ago(30 * h), Bytes: 20},
			{Path: "target", Newest: ago(7 * h), Bytes: 20},
		}},
		{Name: "task-c", Newest: ago(2 * 24 * h), Bytes: 100, Builds: []ScratchBuild{{Path: "dist", Newest: ago(60 * h), Bytes: 10}}},
	}}
	got, err := SweepScratch(context.Background(), host, now, 40)
	if err != nil {
		t.Fatal(err)
	}
	// Retention frees 10 (free 15); builds then free 10 (dist) and 20
	// (node_modules): 45 is over 40, so the 7-hour target stays.
	want := []string{"ses_20261001-000000-aaaaaaaa", "task-c/dist", "task-b/node_modules"}
	if !reflect.DeepEqual(host.removed, want) {
		t.Fatalf("removed %v, want %v", host.removed, want)
	}
	if got.FreedBytes != 40 || got.Folders != 1 || got.Builds != 2 || got.FreeBytes != 45 {
		t.Fatalf("result = %+v", got)
	}
	// Over the floor: only retention runs.
	host = &fakeScratch{free: 500, folders: []ScratchFolder{{Name: "task", Newest: ago(h), Builds: []ScratchBuild{{Path: "target", Newest: ago(99 * h), Bytes: 9}}}}}
	if _, err := SweepScratch(context.Background(), host, now, 40); err != nil || len(host.removed) != 0 {
		t.Fatalf("removed %v, err %v", host.removed, err)
	}
}

// The script is the safety-critical part: in a temporary HOME it lists only
// build outputs that sit beside a project file, and its remove refuses a path
// that escapes the root or crosses a symlink.
func TestScratchScriptListsMarkedBuildsAndRefusesEscapes(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".cache", "dyna", "scratch")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{filepath.Join(root, "task/rust/target"), filepath.Join(root, "task/src/build"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "task/rust/Cargo.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "task/link")); err != nil {
		t.Fatal(err)
	}
	run := func(mode, rel string) (string, error) {
		cmd := exec.Command("bash", "-c", scratchSweepBody)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "DYNA_SCRATCH_MODE=" + mode, "DYNA_SCRATCH_PATH=" + rel}
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := run("list", "")
	if err != nil || !strings.Contains(out, "BUILD\t") || !strings.Contains(out, "\ttask\trust/target\n") || strings.Contains(out, "src/build") {
		t.Fatalf("list = %q, %v", out, err)
	}
	for _, rel := range []string{"../outside", "task/link/keep", "task/link"} {
		if _, err := run("remove", rel); err == nil {
			t.Fatalf("remove %q succeeded", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
	if _, err := run("remove", "task/rust/target"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "task/rust/target")); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
}
