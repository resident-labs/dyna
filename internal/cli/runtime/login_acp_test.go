package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

func TestEnvValueTakesLastEntry(t *testing.T) {
	if got := envValue([]string{"HOME=/a", "X=1", "HOME=/b"}, "HOME"); got != "/b" {
		t.Fatalf("got %q", got)
	}
	if envValue([]string{"HOMEX=/a"}, "HOME") != "" {
		t.Fatal("prefix of another name matched")
	}
}

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

func TestLoginAcpFlagNeedsAntigravityAndAccount(t *testing.T) {
	for _, args := range [][]string{{"login", "codex", "--acp", "--account", "x"}, {"login", "antigravity", "--acp"}} {
		root := NewCommand()
		root.SetArgs(args)
		root.SilenceUsage, root.SilenceErrors = true, true
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "--acp") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
