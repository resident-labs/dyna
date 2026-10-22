package subagent

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/scratch"
)

func TestSessionTurnScratchEnv(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "scratch")
	p := profile.Profile{Name: "mock", Harness: profile.HarnessCustom, Default: true,
		Command: []string{"/bin/sh", "-c", `cat >/dev/null; printf '%s|%s' "$DYNA_SCRATCH" "$(stat -c %a "$DYNA_SCRATCH")" > "$CAPTURE"; printf done`},
		Env:     map[string]string{"CAPTURE": capture}, Taste: 5, Intelligence: 5, Cost: 5}
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

func TestSessionTurnStartsWithScratchUnavailable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DYNA_SCRATCH", "/stale/parent")
	p := profile.Profile{Name: "mock", Harness: profile.HarnessCustom, Default: true,
		Command: []string{"/bin/sh", "-c", `cat >/dev/null; if [ "${DYNA_SCRATCH+x}" = x ]; then printf stale; else printf done; fi`},
		Env:     map[string]string{"DYNA_SCRATCH": "/stale/profile"}, Taste: 5, Intelligence: 5, Cost: 5}
	e := newTestEnv(t, p)
	t.Setenv("TMPDIR", tmp)
	root := scratch.Root()
	if err := os.Mkdir(root, 0700); err != nil {
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
