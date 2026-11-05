package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type serviceCleanupFixture struct {
	host          *Host
	home          string
	removed, kept []string
}

// Execute the real driver's SSH payload locally. File-backed command stubs preserve exit statuses, including explicit missing
// object semantics, so callers can exercise the real Bash failure paths without running Podman or changing the host's HOME.
func newServiceCleanupFixture(t *testing.T) *serviceCleanupFixture {
	t.Helper()
	f := &serviceCleanupFixture{home: t.TempDir()}
	bin := filepath.Join(f.home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubs := map[string]string{
		"systemctl": `#!/bin/bash
printf 'systemctl %s\n' "$*" >> "$HOME/calls"
case "$*" in
  '--user stop '*)
    [ ! -e "$HOME/unit-stop-error" ] || exit 125
    name=${3%.service}; test -e "$HOME/units/$name" || exit 5; rm "$HOME/units/$name" ;;
  '--user daemon-reload') [ ! -e "$HOME/reload-error" ] || exit 125 ;;
  *) exit 90 ;;
esac
`,
		"rm": `#!/bin/bash
case "$*" in
  *'/.config/containers/systemd/'*) [ ! -e "$HOME/file-remove-error" ] || exit 125 ;;
esac
exec /bin/rm "$@"
`,
		"podman": `#!/bin/bash
printf 'podman %s\n' "$*" >> "$HOME/calls"
case "$1 $2" in
  'rm -f')
    [ ! -e "$HOME/container-remove-error" ] || { echo 'container removal failed' >&2; exit 125; }
    shift 2
    ignore=false
    if [ "$1" = --ignore ]; then ignore=true; shift; fi
    [ "$#" = 1 ] && [ -n "$1" ] || exit 90
    if [ ! -e "$HOME/containers/$1" ]; then $ignore; exit; fi
    rm "$HOME/containers/$1" ;;
  'secret ls')
    [ ! -e "$HOME/secret-list-error" ] || { echo 'secret metadata read failed' >&2; exit 125; }
    for s in "$HOME/secrets/"*; do [ ! -f "$s" ] || basename "$s"; done ;;
  'secret rm')
    [ ! -e "$HOME/secret-remove-error" ] || { echo 'secret removal failed' >&2; exit 125; }
    rm "$HOME/secrets/$3" ;;
  'volume rm')
    [ ! -e "$HOME/volume-remove-error" ] || exit 125
    shift 2
    force=false
    while [ "$#" -gt 1 ]; do
      case "$1" in -f|--force) force=true ;; *) exit 90 ;; esac
      shift
    done
    [ "$#" = 1 ] && [ -n "$1" ] || exit 90
    # Podman 5's volume rm --force ignores ErrNoSuchVolume, unlike an unforced remove.
    if [ ! -e "$HOME/volumes/$1" ]; then $force; exit; fi
    rm "$HOME/volumes/$1" ;;
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
	for _, dir := range []string{"units", "containers", "volumes", "secrets", quadletDir} {
		if err := os.MkdirAll(filepath.Join(f.home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		names := []string{service, "dyna-svc-svc_10"}
		if dir == "secrets" {
			names = []string{service + "-TOKEN", service + "-KEY", "dyna-svc-svc_10-TOKEN"}
		} else if dir == quadletDir {
			names = []string{service + ".container", "dyna-svc-svc_10.container"}
		}
		for i, name := range names {
			path := filepath.Join(f.home, dir, name)
			if err := os.WriteFile(path, []byte("exists"), 0o600); err != nil {
				t.Fatal(err)
			}
			if i == len(names)-1 {
				f.kept = append(f.kept, path)
			} else {
				f.removed = append(f.removed, path)
			}
		}
	}
	var err error
	f.host, err = New("user@host", t.TempDir(), "host", &instructionRunner{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRemoveServiceCleansOnlyItsObjectsAndIsIdempotent(t *testing.T) {
	f := newServiceCleanupFixture(t)
	for attempt := range 2 {
		if err := f.host.RemoveService(context.Background(), "svc_1"); err != nil {
			t.Errorf("remove attempt %d: %v", attempt+1, err)
		}
		for _, path := range f.removed {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("remove attempt %d left %s: %v", attempt+1, path, err)
			}
		}
		for _, path := range f.kept {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("remove attempt %d affected unrelated %s: %v", attempt+1, path, err)
			}
		}
	}
	calls, err := os.ReadFile(filepath.Join(f.home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"systemctl --user stop dyna-svc-svc_1.service", "systemctl --user daemon-reload", "podman rm -f --ignore dyna-svc-svc_1", "podman secret rm dyna-svc-svc_1-TOKEN", "podman secret rm dyna-svc-svc_1-KEY"} {
		if !strings.Contains(string(calls), call+"\n") {
			t.Errorf("missing call %q in:\n%s", call, calls)
		}
	}
}

func TestRemoveServicePropagatesCleanupFailuresAndCanRetry(t *testing.T) {
	for _, mode := range []string{"secret-list-error", "container-remove-error", "secret-remove-error", "unit-stop-error", "file-remove-error", "reload-error", "volume-remove-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newServiceCleanupFixture(t)
			failure := filepath.Join(f.home, mode)
			if err := os.WriteFile(failure, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := f.host.RemoveService(context.Background(), "svc_1"); err == nil {
				t.Errorf("%s was swallowed by the actual cleanup payload", mode)
			}
			if err := os.Remove(failure); err != nil {
				t.Fatal(err)
			}
			if err := f.host.RemoveService(context.Background(), "svc_1"); err != nil {
				t.Fatalf("retry after %s: %v", mode, err)
			}
			for _, path := range f.removed {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("successful retry left %s: %v", path, err)
				}
			}
			for _, path := range f.kept {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("cleanup affected unrelated %s: %v", path, err)
				}
			}
		})
	}
}
