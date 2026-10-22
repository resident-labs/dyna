package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

func TestAntigravityACPLoginNeedsInstalledServer(t *testing.T) {
	scope := filepath.Join(t.TempDir(), ".runtime")
	flow := &harnessruntime.NativeLogin{Cwd: scope, Env: []string{"HOME=" + harnesshome.Home(scope)}}
	err := antigravityACPLogin(&cobra.Command{}, filepath.Join(t.TempDir(), "agy"), "google:x", flow)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing server was not reported: %v", err)
	}
}

func TestAntigravityACPLoginRefusesHomeOutsidePairedScope(t *testing.T) {
	scope := filepath.Join(t.TempDir(), ".runtime")
	for _, home := range []string{scope, filepath.Join(t.TempDir(), "other"), ""} {
		flow := &harnessruntime.NativeLogin{Cwd: scope, Env: []string{"HOME=" + home}}
		err := antigravityACPLogin(&cobra.Command{}, filepath.Join(t.TempDir(), "agy"), "google:x", flow)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("home %q was accepted: %v", home, err)
		}
	}
}
