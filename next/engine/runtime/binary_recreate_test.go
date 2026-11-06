package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// Native command/filesystem fixture, not live Podman: named workspace storage is separate from disposable container files.
func TestStaleBinaryPinRecreatesUsingTheSameNamedWorkspaceVolume(t *testing.T) {
	root, tools := t.TempDir(), t.TempDir()
	layout := box.Layout{Data: root, HostData: root}
	bin := layout.HostBin()
	volume, containerDir, log := filepath.Join(root, "volume"), filepath.Join(root, "container"), filepath.Join(root, "commands")
	if err := os.MkdirAll(volume, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(containerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volume, "work-file"), []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(containerDir, "old-layer"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	oldSum := stagePinnedFixture(t, bin, []byte("#!/bin/bash\nprintf OLD > \"$RECREATE_VOLUME/woke-on\"\n"))
	newSum := stagePinnedFixture(t, bin, []byte("#!/bin/bash\nprintf NEW > \"$RECREATE_VOLUME/woke-on\"\n"))
	if err := os.WriteFile(filepath.Join(containerDir, "sum"), []byte(oldSum), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$RECREATE_LOG"
case " $* " in
 *' ps '*) sum=$(cat "$RECREATE_CONTAINER/sum"); printf '[{"Labels":{"dyna.box":"box_a","dyna.layout":"` + box.ContainerLayout + `","dyna.binary":"%s"},"State":"exited"}]' "$sum" ;;
 *' volume rm '*) rm -rf -- "$RECREATE_VOLUME" ;;
 *' rm '*) rm -rf -- "$RECREATE_CONTAINER" ;;
 *' create '*)
  mkdir -p -- "$RECREATE_CONTAINER" "$RECREATE_VOLUME"
  workspace=0
  for arg in "$@"; do
   if [[ $arg == dyna-box-box_a:/box ]]; then workspace=1; fi
   if [[ $arg == dyna.binary=* ]]; then printf '%s' "${arg#dyna.binary=}" > "$RECREATE_CONTAINER/sum"; fi
   if [[ $arg == *:/opt/dyna/bin/current:ro ]]; then printf '%s' "${arg%:/opt/dyna/bin/current:ro}/dyna" > "$RECREATE_CONTAINER/executable"; fi
  done
  [ "$workspace" -eq 1 ] ;;
 *' start '*) exec "$(cat "$RECREATE_CONTAINER/executable")" ;;
 *) : ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "systemctl"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("RECREATE_VOLUME", volume)
	t.Setenv("RECREATE_CONTAINER", containerDir)
	t.Setenv("RECREATE_LOG", log)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	h, err := podman.New("user@host", t.TempDir(), "arch", &nativePinnedRunner{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: inspectStore{restoreStore{hosts: engineHosts("arch")}}, Layout: layout, hosts: map[contract.HostName]*podman.Host{"arch": h}, restored: map[contract.HostName]bool{"arch": true}, installed: map[contract.HostName]bool{"arch": true}}
	b.binOnce.Do(func() { b.binSum = newSum })
	status, found, err := b.Inspect(context.Background(), "box_a")
	if err != nil || !found || !status.StaleLayout || status.Binary != oldSum {
		t.Fatalf("old binary pin was not stale: %+v, %v, %v", status, found, err)
	}
	// The existing lifecycle stale-layout path invokes Recreate at a normal start/refresh point, not during a turn.
	size, _ := box.Size(contract.BoxSizeAgent)
	if err := b.Recreate(context.Background(), lifecycle.BoxSpec{Box: "box_a", Session: "ses_a", Room: "room_1", Host: "arch", Slice: "dyna-recreate-test.slice", Size: size}, "same-image"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"work-file": "kept", "woke-on": "NEW"} {
		raw, err := os.ReadFile(filepath.Join(volume, name))
		if err != nil || string(raw) != want {
			t.Fatalf("workspace %s = %s, %v; wanted %s", name, raw, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(containerDir, "old-layer")); !os.IsNotExist(err) {
		t.Fatalf("old container layer survived: %v", err)
	}
	status, found, err = b.Inspect(context.Background(), "box_a")
	if err != nil || !found || status.StaleLayout || status.Binary != newSum {
		t.Fatalf("recreated binary pin is not current: %+v, %v, %v", status, found, err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "volume rm") {
		t.Fatalf("binary recreation removed a named volume: %s", raw)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("%s:/opt/dyna/bin/current:ro", filepath.Join(bin, newSum))) {
		t.Fatalf("new binary was not pinned in create: %s", raw)
	}
}
