package runtime

import (
	"os"
	"path/filepath"
	"testing"

	harnessmgr "dyna-agent/internal/harness/config"
)

// cleanupRuntime builds a Runtime with a fake account tree and a generation
// root under one temporary directory. No account store, harness or login is
// involved.
func cleanupRuntime(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	r := &Runtime{
		Accounts:    &Accounts{Root: filepath.Join(root, "accounts")},
		Config:      &harnessmgr.ConfigManager{Path: filepath.Join(root, "config", "dyna", "harness-config.json")},
		Generations: filepath.Join(root, "generations"),
	}
	for _, dir := range []string{r.Accounts.Root, r.Generations, filepath.Dir(r.Config.Path)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func needDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func needLink(t *testing.T, target, link string) {
	t.Helper()
	needDir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to stay: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone: %v", path, err)
	}
}

func TestEnsureCatalogRemovesOnlyDynaSkillLinks(t *testing.T) {
	r := cleanupRuntime(t)
	generation := filepath.Join(r.Generations, "gen-v1-fixture")
	needDir(t, filepath.Join(generation, "skills", "base"))
	runtimeHome := filepath.Join(r.Accounts.Root, "pi", ".runtime")
	accountHome := filepath.Join(r.Accounts.Root, "claude-code", "account-1")
	userSkill := filepath.Join(t.TempDir(), "user-skill")
	needDir(t, userSkill)

	needDir(t, filepath.Join(runtimeHome, "skills"))
	needLink(t, filepath.Join(generation, "skills", "base"), filepath.Join(runtimeHome, "skills", "base"))
	needLink(t, userSkill, filepath.Join(runtimeHome, "skills", "mine"))
	needDir(t, filepath.Join(runtimeHome, "skills", "notes"))
	needDir(t, filepath.Join(accountHome, "skills"))
	needLink(t, filepath.Join(generation, "skills", "base"), filepath.Join(accountHome, "skills", "base"))
	// The legacy whole-view link pointed at one generation's skills directory.
	needLink(t, filepath.Join(generation, "skills"), filepath.Join(r.Accounts.Root, "grok", ".runtime", "skills"))
	// The seeded orchestrator skill beside the catalog.
	seeded := filepath.Join(filepath.Dir(r.Config.Path), "skills", "load-dyna-orchestrator")
	needDir(t, seeded)

	for range 2 {
		if err := r.EnsureCatalog(); err != nil {
			t.Fatal(err)
		}
		mustNotExist(t, filepath.Join(runtimeHome, "skills", "base"))
		mustNotExist(t, filepath.Join(accountHome, "skills", "base"))
		mustNotExist(t, filepath.Join(r.Accounts.Root, "grok", ".runtime", "skills"))
		mustNotExist(t, seeded)
		mustExist(t, filepath.Join(runtimeHome, "skills", "mine"))
		mustExist(t, filepath.Join(runtimeHome, "skills", "notes"))
		mustExist(t, filepath.Join(runtimeHome, "skills"))
		mustExist(t, filepath.Join(accountHome, "skills"))
		if !filepath.IsAbs(userSkill) {
			t.Fatal("fixture user skill is not absolute")
		}
		if _, err := os.Stat(userSkill); err != nil {
			t.Fatalf("cleanup touched the user skill: %v", err)
		}
	}
}

func TestEnsureCatalogRefusesUnsafeSkillPaths(t *testing.T) {
	r := cleanupRuntime(t)
	userSkill := filepath.Join(t.TempDir(), "user-skill")
	needDir(t, userSkill)

	// A skills view that is not Dyna's link stays, whatever it points at.
	outside := filepath.Join(r.Accounts.Root, "pi", ".runtime", "skills")
	needLink(t, userSkill, outside)
	// A regular file is never a view.
	plain := filepath.Join(r.Accounts.Root, "kimi-code", ".runtime", "skills")
	needDir(t, filepath.Dir(plain))
	if err := os.WriteFile(plain, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	// A link that escapes its view with ".." stays.
	escaped := filepath.Join(r.Accounts.Root, "pi", ".runtime", "skills-escape")
	needDir(t, escaped)
	needLink(t, filepath.Join("..", "..", "..", "..", "elsewhere"), filepath.Join(escaped, "elsewhere"))
	// A harness entry that is itself a link is not Dyna's account tree.
	realHarness := filepath.Join(t.TempDir(), "pi")
	needDir(t, filepath.Join(realHarness, ".runtime", "skills"))
	needLink(t, userSkill, filepath.Join(realHarness, ".runtime", "skills", "mine"))
	needLink(t, realHarness, filepath.Join(r.Accounts.Root, "pi", "linked-harness"))

	if err := r.EnsureCatalog(); err != nil {
		t.Fatal(err)
	}
	mustExist(t, outside)
	mustExist(t, plain)
	mustExist(t, filepath.Join(escaped, "elsewhere"))
	mustExist(t, filepath.Join(realHarness, ".runtime", "skills", "mine"))
	if raw, err := os.ReadFile(plain); err != nil || string(raw) != "mine" {
		t.Fatalf("cleanup changed a real file: %s %v", raw, err)
	}
	if target, err := os.Readlink(outside); err != nil || target != userSkill {
		t.Fatalf("cleanup retargeted the view: %q %v", target, err)
	}
}
