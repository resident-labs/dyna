package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
)

func TestAntigravityACPTokenIsCheckedAndMadePrivate(t *testing.T) {
	scope := filepath.Join(t.TempDir(), ".runtime")
	token := filepath.Join(harnesshome.Home(scope), native.AntigravityACPTokenFile)
	if err := os.MkdirAll(filepath.Dir(token), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateAccountHome("antigravity", scope, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(token)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("ACP token mode was not made private: %v", err)
	}
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", token); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateAccountHome("antigravity", scope, false); err == nil {
		t.Fatal("a symlinked ACP token was accepted")
	}
}
