package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/testsafe"
)

func TestGitIncludesIgnoreAttributesAndTilde(t *testing.T) {
	git, err := executable("/usr/bin/git")
	if err != nil {
		t.Skip("git unavailable")
	}
	scope, owner, env := fixture(t)
	config := filepath.Join(owner, "xdg config ' $ ")
	env = append(env, "XDG_CONFIG_HOME="+config, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0")
	env = testsafe.GitConfigWithoutAutoMaintenance(env)
	// Use actual git, while dyna remains a fake executable.
	fakeBin := envMap(env)["PATH"]
	if err := os.Remove(filepath.Join(fakeBin, "git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(fakeBin, "git")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(config, "git/config"), "[user]\n name = XDG identity\n[include]\n path = ~/tilde-config\n", 0600)
	write(t, filepath.Join(owner, "tilde-config"), "[test]\n tilde = owner\n", 0600)
	write(t, filepath.Join(owner, ".gitconfig"), "[user]\n name = HOME identity\n signingKey = OWNERKEY\n[commit]\n gpgSign = true\n", 0600)
	write(t, filepath.Join(config, "git/ignore"), "ignored-file\n", 0600)
	write(t, filepath.Join(config, "git/attributes"), "*.example custom=owner\n", 0600)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	privateEnv := merged(env, out)
	bridge := filepath.Join(filepath.Dir(out["DYNA_BIN"]), "git")
	if got := run(t, privateEnv, bridge, "config", "--global", "--includes", "--get", "user.name"); got != AgentGitName+"\n" {
		t.Fatalf("agent commits took the owner's identity: %q", got)
	}
	if got := run(t, privateEnv, bridge, "config", "--global", "--includes", "--type=bool", "--get", "commit.gpgSign"); got != "false\n" {
		t.Fatalf("agent commits are signed with the owner's setting: %q", got)
	}
	if got := run(t, privateEnv, bridge, "config", "--global", "--includes", "--get", "user.email"); got != AgentGitEmail+"\n" {
		t.Fatalf("agent email = %q", got)
	}
	if got := run(t, privateEnv, bridge, "config", "--global", "--includes", "--get", "test.tilde"); got != "owner\n" {
		t.Fatal(got)
	}
	// Only this test's disposable repository receives Git writes.
	repo := filepath.Join(t.TempDir(), "repo")
	run(t, privateEnv, bridge, "init", "-q", repo)
	write(t, filepath.Join(repo, "ignored-file"), "ignored", 0600)
	if got := run(t, privateEnv, bridge, "-C", repo, "check-ignore", "ignored-file"); got != "ignored-file\n" {
		t.Fatal(got)
	}
	if got := run(t, privateEnv, bridge, "-C", repo, "check-attr", "custom", "--", "a.example"); got != "a.example: custom: owner\n" {
		t.Fatal(got)
	}
	// Explicit owner values override fallbacks because includes come last.
	write(t, filepath.Join(owner, ".gitconfig"), "[core]\n excludesFile = ~/custom-ignore\n attributesFile = ~/custom-attributes\n", 0600)
	write(t, filepath.Join(owner, "custom-ignore"), "custom-ignored\n", 0600)
	write(t, filepath.Join(owner, "custom-attributes"), "*.example custom=explicit\n", 0600)
	if got := run(t, privateEnv, bridge, "-C", repo, "check-ignore", "custom-ignored"); got != "custom-ignored\n" {
		t.Fatal(got)
	}
	if got := run(t, privateEnv, bridge, "-C", repo, "check-attr", "custom", "--", "a.example"); got != "a.example: custom: explicit\n" {
		t.Fatal(got)
	}
	for _, value := range []string{filepath.Join(owner, "only-config"), ""} {
		t.Run(map[bool]string{true: "empty", false: "explicit"}[value == ""], func(t *testing.T) {
			if value != "" {
				write(t, value, "[test]\n selected = explicit\n", 0600)
			}
			next, err := Prepare(scope, "", append(env, "GIT_CONFIG_GLOBAL="+value))
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(publishedConfig(next))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(contents), gitQuote(filepath.Join(owner, ".gitconfig"))) || strings.Contains(string(contents), gitQuote(filepath.Join(config, "git/config"))) {
				t.Fatal("owner default included despite explicit override")
			}
			if value != "" {
				if got := run(t, merged(env, next), filepath.Join(filepath.Dir(next["DYNA_BIN"]), "git"), "config", "--global", "--includes", "--get", "test.selected"); got != "explicit\n" {
					t.Fatal(got)
				}
			} else if strings.Contains(string(contents), "[include]") {
				t.Fatal("explicit empty global must include nothing")
			}
		})
	}
}

func TestToolRootsOnlyAndTrustedDyna(t *testing.T) {
	scope, owner, env := fixture(t)
	out, err := ToolRoots(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "PATH", "DYNA_BIN", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "XDG_CONFIG_HOME"} {
		if _, ok := out[key]; ok {
			t.Fatalf("ToolRoots unexpectedly sets %s", key)
		}
	}
	want := map[string]string{
		"MISE_CONFIG_DIR": filepath.Join(owner, ".config/mise"), "MISE_DATA_DIR": filepath.Join(owner, ".local/share/mise"), "MISE_CACHE_DIR": filepath.Join(envMap(env)["XDG_CACHE_HOME"], "mise"),
		"RUSTUP_HOME": filepath.Join(owner, ".rustup"), "CARGO_HOME": filepath.Join(owner, ".cargo"), "GOPATH": filepath.Join(owner, "go"), "GOMODCACHE": filepath.Join(owner, "go/pkg/mod"),
		"NPM_CONFIG_USERCONFIG": filepath.Join(owner, ".npmrc"), "BUN_INSTALL": filepath.Join(owner, ".bun"), "PNPM_HOME": filepath.Join(owner, ".local/share/pnpm"),
	}
	for key, value := range want {
		if out[key] != value {
			t.Fatalf("%s: got %q want %q", key, out[key], value)
		}
	}
	for key := range out {
		env = append(env, key+"=explicit ' ")
	}
	explicit, err := ToolRoots(env)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range explicit {
		if value != "explicit ' " && key != "ENV" && key != "BASH_ENV" && key != "ZDOTDIR" {
			t.Fatalf("%s: %q", key, value)
		}
	}
	if _, err := Prepare(scope, "/fake/dyna", env); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(scope, "relative/dyna", env); err == nil {
		t.Fatal("relative realDyna accepted")
	}
}
