package localci

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Parallel checks share one bare cache, but git names a worktree's metadata
// after the checkout path's final segment. Two checkouts named work therefore
// remove and recreate the same worktrees/work entry. Force that interleaving
// without a sleep: the first fake add waits while the second one collides.
func TestParallelChecksUseDistinctLockedWorktrees(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	control := filepath.Join(dir, "control")
	cache := filepath.Join(dir, "cache.git")
	for _, path := range []string{bin, control, filepath.Join(dir, "metadata")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The fake derives the metadata name exactly as git does. An unlocked first
	// add waits for the deterministic collision. A correctly locked add does not
	// wait, because its peer cannot enter the metadata operation until it ends.
	fakeGit := `#!/bin/bash
set -u
cache=${1#--git-dir=}
operation=$3
metadata="` + filepath.Join(dir, "metadata") + `"
control="` + control + `"
lock="$cache.worktrees.lock"
locked=0
flock -n "$lock" true 2>/dev/null || locked=1
if [ "$locked" = 0 ]; then
  printf '%s\n' "$operation" >> "$control/unlocked"
fi
case "$operation" in
add)
  work=$5
  id=${work##*/}
  if ! mkdir "$metadata/$id" 2>/dev/null; then
    printf 'fatal: failed to read %s/worktrees/%s/commondir: Success\n' "$cache" "$id" >&2
    exit 1
  fi
  printf '%s\n' "$id" >> "$control/added-names"
  if mkdir "$control/first" 2>/dev/null; then
    printf 'ready\n' > "$control/ready"
    if [ "$locked" = 0 ]; then
      read -r _ < "$control/release"
    fi
  fi
  ;;
remove)
  rm -rf "$metadata/${5##*/}"
  ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fakeGit), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeDocker := "#!/bin/sh\nexit 0\n"
	for _, name := range []string{"docker", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fakeDocker), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ready", "release"} {
		if err := syscall.Mkfifo(filepath.Join(control, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A read-write descriptor keeps the release write from blocking when the
	// fixed, locked first add has no reason to wait.
	release, err := os.OpenFile(filepath.Join(control, "release"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()

	base := filepath.Join(dir, "base")
	runDir := filepath.Join(base, "runs", "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(runDir, ".active")
	if err := os.WriteFile(active, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	newCommand := func(id Suite, index int) *exec.Cmd {
		check := nodeCheck{id: id, name: CheckName(id), inner: "true", image: "image"}
		script := nodeCheckScript(Node{Name: "local"}, base, cache, runDir, "run", strings.Repeat("a", 40), "", check, time.Minute, index, false, DefaultActImage, "", "")
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		return cmd
	}
	first := newCommand("first", 0)
	second := newCommand("second", 1)
	var firstOutput, secondOutput bytes.Buffer
	first.Stdout, first.Stderr = &firstOutput, &firstOutput
	second.Stdout, second.Stderr = &secondOutput, &secondOutput
	firstDone := make(chan error, 1)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { firstDone <- first.Wait() }()
	_, readyErr := os.ReadFile(filepath.Join(control, "ready"))
	secondErr := second.Run()
	_, _ = release.WriteString("go\n")
	firstErr := <-firstDone
	if readyErr != nil {
		t.Fatalf("the first checkout did not reach its worktree add: %v", readyErr)
	}
	if firstErr != nil || secondErr != nil {
		t.Fatalf("parallel checkouts: first=%v: %s; second=%v: %s", firstErr, firstOutput.String(), secondErr, secondOutput.String())
	}
	if unlocked, err := os.ReadFile(filepath.Join(control, "unlocked")); err == nil {
		t.Fatalf("git mutated worktree metadata without the repository lock: %s", unlocked)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(control, "added-names"))
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Fields(string(data))
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("worktree metadata names = %v, want two distinct names", names)
	}
}
