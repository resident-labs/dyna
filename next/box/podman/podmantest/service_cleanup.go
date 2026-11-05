// Package podmantest provides file-backed test fixtures for the real Podman driver, without opening SSH connections.
package podmantest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box/podman"
)

const quadletDir = ".config/containers/systemd"

// localPayloadRunner acknowledges SSH control operations and executes the driver's actual framed command locally.
type localPayloadRunner struct{}

func (localPayloadRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Output()
}

// ServiceCleanup runs the real driver against file-backed objects in a private HOME.
type ServiceCleanup struct {
	Host          *podman.Host
	Home          string
	Removed, Kept []string
}

// Execute the real driver's SSH payload locally. File-backed command stubs preserve exit statuses, including explicit missing
// object semantics, so callers can exercise the real Bash failure paths without running Podman or changing the host's HOME.
func NewServiceCleanup(t *testing.T) *ServiceCleanup {
	t.Helper()
	f := &ServiceCleanup{Home: t.TempDir()}
	commands := filepath.Join(f.Home, "commands")
	if err := os.MkdirAll(commands, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.Home)
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
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
		if err := os.WriteFile(filepath.Join(commands, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const service = "dyna-svc-svc_1"
	for _, dir := range []string{"units", "containers", "volumes", "secrets", quadletDir} {
		if err := os.MkdirAll(filepath.Join(f.Home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		names := []string{service, "dyna-svc-svc_10"}
		if dir == "secrets" {
			names = []string{service + "-TOKEN", service + "-KEY", "dyna-svc-svc_10-TOKEN"}
		} else if dir == quadletDir {
			names = []string{service + ".container", "dyna-svc-svc_10.container"}
		}
		for i, name := range names {
			path := filepath.Join(f.Home, dir, name)
			if err := os.WriteFile(path, []byte("exists"), 0o600); err != nil {
				t.Fatal(err)
			}
			if i == len(names)-1 {
				f.Kept = append(f.Kept, path)
			} else {
				f.Removed = append(f.Removed, path)
			}
		}
	}
	var err error
	f.Host, err = podman.New("user@host", t.TempDir(), "host", localPayloadRunner{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
