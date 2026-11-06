package podman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

// Only the prerequisite command runs natively; other registration probes get an
// empty framed reply, so this test never invokes a live Podman or SSH service.
type prerequisiteCheckRunner struct{ nativeShellRunner }

func (r prerequisiteCheckRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	if strings.Contains(args[len(args)-1], "command -v pgrep") {
		return r.nativeShellRunner.Run(ctx, in, args...)
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestHostCheckRequiresPgrep(t *testing.T) {
	tools := t.TempDir()
	// Real frame tools, with only pgrep absent. Missing pgrep must not be
	// confused with a failed transport or a missing tool in the frame itself.
	for _, name := range []string{"bash", "mktemp", "head", "wc", "cat", "rm"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		t.Fatal(err)
	}
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("present_%v", present), func(t *testing.T) {
			if present {
				if err := os.Symlink(pgrep, filepath.Join(tools, "pgrep")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools)
			h, err := New("owner@host", t.TempDir(), "h", nativeShellRunner{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			err = h.CheckExec(context.Background())
			t.Logf("exec prerequisite with pgrep present=%v: %v", present, err)
			if present && err != nil {
				t.Fatalf("host with pgrep rejected: %v", err)
			}
			if !present && (err == nil || !strings.Contains(err.Error(), "pgrep is missing: install procps")) {
				t.Fatalf("missing pgrep must fail visibly, got %v", err)
			}
			// Box hosts must run the same prerequisite, not just exec-only hosts.
			h, err = New("owner@host", t.TempDir(), "h", prerequisiteCheckRunner{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			result := h.Check(context.Background(), CheckOptions{Layout: box.Layout{HostData: root}, Runtime: root, AdminOnly: true, EngineHost: true})
			for _, item := range result.Items {
				if item.Name == CheckExec {
					if item.OK != present || (!present && !strings.Contains(item.Reason, "pgrep is missing: install procps")) {
						t.Fatalf("registration prerequisite: %+v", item)
					}
					return
				}
			}
			t.Fatal("box host registration omitted the cancellation prerequisite")
		})
	}
}
