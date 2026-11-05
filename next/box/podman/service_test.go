package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the real driver's SSH payload locally. The command stubs model objects by files in a temporary HOME; unlike the
// fake service driver this exercises bash's positional arguments, the cleanup script and the remote exit status end to end.
func TestRemoveServiceCleansOnlyItsObjectsAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubs := map[string]string{
		"systemctl": `#!/bin/bash
printf 'systemctl %s\n' "$*" >> "$HOME/calls"
case "$*" in
  '--user stop '*) name=${3%.service}; test -e "$HOME/units/$name" || exit 5; rm "$HOME/units/$name" ;;
  '--user daemon-reload') ;;
  *) exit 90 ;;
esac
`,
		"podman": `#!/bin/bash
printf 'podman %s\n' "$*" >> "$HOME/calls"
case "$1 $2" in
  'rm -f') test -e "$HOME/containers/$3" || exit 1; rm "$HOME/containers/$3" ;;
  'secret ls') for s in "$HOME/secrets/"*; do [ ! -f "$s" ] || basename "$s"; done ;;
  'secret rm') rm "$HOME/secrets/$3" ;;
  'volume rm')
    shift 2
    force=false
    while [ "$#" -gt 1 ]; do
      case "$1" in -f|--force) force=true ;; *) exit 90 ;; esac
      shift
    done
    [ "$#" = 1 ] && [ -n "$1" ] || exit 90
    # Podman 5's volume rm --force ignores ErrNoSuchVolume, unlike an unforced remove.
    if [ ! -e "$HOME/volumes/$1" ]; then $force; exit; fi
    rm "$HOME/volumes/$1"
    ;;
  *) exit 90 ;;
esac
`,
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const service = "dyna-svc-svc_1"
	var removed, kept []string
	for _, dir := range []string{"units", "containers", "volumes", "secrets", quadletDir} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		names := []string{service, "dyna-svc-svc_10"}
		if dir == "secrets" {
			names = []string{service + "-TOKEN", service + "-KEY", "dyna-svc-svc_10-TOKEN"}
		} else if dir == quadletDir {
			names = []string{service + ".container", "dyna-svc-svc_10.container"}
		}
		for i, name := range names {
			path := filepath.Join(home, dir, name)
			if err := os.WriteFile(path, []byte("exists"), 0o600); err != nil {
				t.Fatal(err)
			}
			if i == len(names)-1 {
				kept = append(kept, path)
			} else {
				removed = append(removed, path)
			}
		}
	}
	h, err := New("user@host", t.TempDir(), "host", &instructionRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := h.RemoveService(context.Background(), "svc_1"); err != nil {
			t.Errorf("remove attempt %d: %v", attempt+1, err)
		}
		for _, path := range removed {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("remove attempt %d left %s: %v", attempt+1, path, err)
			}
		}
		for _, path := range kept {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("remove attempt %d affected unrelated %s: %v", attempt+1, path, err)
			}
		}
	}
	calls, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"systemctl --user stop " + service + ".service", "systemctl --user daemon-reload", "podman rm -f " + service, "podman secret rm " + service + "-TOKEN", "podman secret rm " + service + "-KEY"} {
		if !strings.Contains(string(calls), call+"\n") {
			t.Errorf("missing call %q in:\n%s", call, calls)
		}
	}
}
