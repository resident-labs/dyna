package subagent

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/scratch"
)

func TestSessionTurnScratchEnv(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "scratch")
	p := fakeworker.InstallClaude(t, `printf '%s|%s' "$DYNA_SCRATCH" "$(stat -c %a "$DYNA_SCRATCH")" > "$CAPTURE"; printf done`)
	p.Name, p.Default = "mock", true
	p.Env = mergedEnv(p.Env, map[string]string{"CAPTURE": capture})
	e := newTestEnv(t, p)
	e.joinRunners()
	info, turn := e.start("check scratch")
	_, done := e.wait(info.ID, turn.N)
	if done.Error != "" {
		t.Fatal(done.Error)
	}
	path := filepath.Join(scratch.Root(), info.ID)
	got, err := os.ReadFile(capture)
	if err != nil || string(got) != path+"|700" {
		t.Fatalf("scratch env %q, %v", got, err)
	}
	if !filepath.IsAbs(path) {
		t.Fatal(path)
	}
}

// A session's temporary files go to its disk scratch folder, never to the host
// temporary folder, which is a RAM disk on many hosts. Shell jobs, tests and Go
// builds the session starts inherit these variables.
func TestSessionTurnTemporaryFolderIsOnDisk(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "temp")
	p := fakeworker.InstallClaude(t, `printf '%s|%s|%s|%s' "$TMPDIR" "$TMP" "$TEMP" "$GOTMPDIR" > "$CAPTURE"; printf done`)
	p.Name, p.Default = "mock", true
	p.Env = mergedEnv(p.Env, map[string]string{"CAPTURE": capture, "TMPDIR": "/tmp"})
	e := newTestEnv(t, p)
	e.joinRunners()
	info, turn := e.start("check temporary folder")
	_, done := e.wait(info.ID, turn.N)
	if done.Error != "" {
		t.Fatal(done.Error)
	}
	dir := filepath.Join(scratch.Root(), info.ID, scratch.TempDirName)
	got, err := os.ReadFile(capture)
	if want := dir + "|" + dir + "|" + dir + "|" + dir; err != nil || string(got) != want {
		t.Fatalf("temporary folder env %q, %v; want %q", got, err, want)
	}
}

func TestSessionTurnStartsWithScratchUnavailable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DYNA_SCRATCH", "/stale/parent")
	p := fakeworker.InstallClaude(t, `if [ "${DYNA_SCRATCH+x}" = x ]; then printf stale; else printf done; fi`)
	p.Name, p.Default = "mock", true
	p.Env = mergedEnv(p.Env, map[string]string{"DYNA_SCRATCH": "/stale/profile"})
	e := newTestEnv(t, p)
	t.Setenv("TMPDIR", tmp)
	root := scratch.Root()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	e.joinRunners()
	info, turn := e.start("scratch is unavailable")
	_, done := e.wait(info.ID, turn.N)
	if done.Error != "" || done.Result != "done" {
		t.Fatalf("turn did not run without scratch: %+v", done)
	}
}
