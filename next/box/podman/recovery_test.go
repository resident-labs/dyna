package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestRemovingRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, state, inspect string
		recover              bool
	}{
		{"removing", "removing", "removing", true},
		{"inspect-removing", "exited", "removing", true},
		{"store-missing", "exited", "missing", true},
		{"store-not-known", "exited", "not-known", true},
		{"running", "running", "missing", false},
		{"paused", "paused", "missing", false},
		{"unknown", "exited", "unknown", false},
		{"transport", "exited", "transport", false},
		{"wrong-name", "other", "missing", false},
		{"ps-transport", "transport", "missing", false},
		{"multiple-names", "multiple", "missing", false},
		{"running-disagrees", "running", "removing", false},
		{"paused-disagrees", "paused", "removing", false},
	} {
		for _, mode := range []string{"recreate", "create", "warm"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root, h := namespaceFixture(t)
				tools := t.TempDir()
				t.Setenv("REC_STATE", tc.state)
				t.Setenv("REC_INSPECT", tc.inspect)
				t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
				script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$NS_ROOT/recovery-calls"
[ "${1:-}" != --cgroup-manager=systemd ] || shift
case "$1" in
 inspect)
 [ "$2 $3" = '--type container' ] || { echo 'Error: no such object: "b"' >&2; exit 125; }
 case "$REC_INSPECT" in
 missing) echo 'Error: no container with name or ID "b" found: no such container' >&2; exit 125;;
 not-known) echo 'Error: container b not known' >&2; exit 125;;
 unknown) echo 'Error: database broken' >&2; exit 125;;
 transport) exit 255;;
 *) echo "$REC_INSPECT";; esac;;
 ps) [ "$REC_STATE" != transport ] || exit 255
 if [ "$REC_STATE" = multiple ]; then printf 'b|removing\nb-other|removing\n'; exit 0; fi
 if [ "$REC_STATE" = other ]; then echo 'b-other|removing'; else echo "b|$REC_STATE"; fi;;
 rm)
 [ "$*" = 'rm -f b' ] || exit 91
 test -s "$NS_ROOT/locks/box-b.epoch" || exit 93
 if flock -n "$NS_ROOT/locks/box-b.lock" true; then exit 94; fi
 touch "$NS_ROOT/recovered";;
 start|stop) test -f "$NS_ROOT/recovered" || { echo 'container state improper' >&2; exit 125; };;
 create) test -f "$NS_ROOT/recovered" || exit 125;;
 pod) :;;
 *) exit 92;;
esac
`
				if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				for _, marker := range []string{"workspace-marker", "stack-marker", "neighbor-marker"} {
					if err := os.WriteFile(filepath.Join(root, marker), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				spec := box.ContainerSpec{Box: "b", Image: "image"}
				switch mode {
				case "recreate":
					err = h.CreateBox(context.Background(), spec, nil, true)
				case "create":
					err = h.CreateContainer(context.Background(), spec)
				case "warm":
					if e := h.StartContainer(context.Background(), "b"); e == nil {
						t.Fatal("warm orphan removal must retain failed start for reconciliation")
					}
					err = h.CreateBox(context.Background(), spec, nil, false)
				}
				if (err == nil) != tc.recover {
					t.Fatalf("recovered=%v error=%v", tc.recover, err)
				}
				calls, _ := os.ReadFile(filepath.Join(root, "recovery-calls"))
				if strings.Contains(string(calls), "rm -f") != tc.recover {
					t.Fatalf("unexpected removal: %s", calls)
				}
				if strings.Contains(string(calls), "--volumes") || strings.Contains(string(calls), "prune") {
					t.Fatalf("volume mutation: %s", calls)
				}
				for _, marker := range []string{"workspace-marker", "stack-marker", "neighbor-marker"} {
					if b, e := os.ReadFile(filepath.Join(root, marker)); e != nil || string(b) != "keep" {
						t.Fatalf("marker %s lost: %v", marker, e)
					}
				}
			})
		}
	}
}

// Warm reconciliation uses ListContainers, not inspect. A Removing record must
// select Recreate even when its image, layout and binary labels are current.
func TestRemovingSelectsWarmRecreate(t *testing.T) {
	r := &runner{body: `[{"Labels":{"dyna.box":"b","dyna.layout":"` + box.ContainerLayout + `"},"State":"removing"},{"Labels":{"dyna.box":"healthy","dyna.layout":"` + box.ContainerLayout + `"},"State":"running"}]`}
	statuses, err := newTestHost(t, r).ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || !statuses[0].StaleLayout || statuses[0].Running || statuses[1].StaleLayout || !statuses[1].Running {
		t.Fatalf("warm reconciliation statuses: %+v", statuses)
	}
}
