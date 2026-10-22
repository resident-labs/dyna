package ghclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSessionUsesTheRoomAccount(t *testing.T) {
	records := setupRooms(t, "resident-labs/personal", "work/private")
	t.Setenv("GH_REPO", "unscoped/other")
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"personal"},{"login":"work"}]}}';;
"auth token") case "$*" in *personal*) echo personal-secret;; *work*) echo work-secret;; esac;;
"api --hostname") case "$GH_TOKEN:$4" in personal-secret:repos/resident-labs/personal|work-secret:repos/work/private) echo '{"permissions":{"push":true}}';; *) echo "gh: Not Found (HTTP 404)" >&2; exit 1;; esac;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for i, rec := range records {
		scratch := t.TempDir()
		env, err := SessionEnvironment(context.Background(), rec.ID, scratch, "/real/dyna", os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"personal-secret", "work-secret"}[i]
		if env["GH_TOKEN"] != want || env["GH_HOST"] != "github.com" || env["GH_REPO"] != "" {
			t.Fatal("session has the wrong repository credentials")
		}
		target, err := os.Readlink(filepath.Join(scratch, "github-bin", "gh"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (target != filepath.Join(bin, "gh") || env[ReadOnlyEnv] != "") {
			t.Fatal("write room did not receive plain gh")
		}
		if i == 1 && (target != "/real/dyna" || env[ReadOnlyEnv] != "1") {
			t.Fatal("read-only room did not receive the gh guard")
		}
		inherited := append([]string{}, os.Environ()...)
		for k, v := range env {
			inherited = append(inherited, k+"="+v)
		}
		t.Setenv("PATH", env["PATH"])
		t.Setenv(BinaryEnv, env[BinaryEnv])
		t.Setenv(ReadOnlyEnv, env[ReadOnlyEnv])
		child, err := SessionEnvironment(context.Background(), rec.ID, t.TempDir(), "/real/dyna", inherited)
		if err != nil {
			t.Fatal(err)
		}
		if child["GH_TOKEN"] != want || child["GIT_CONFIG_COUNT"] != env["GIT_CONFIG_COUNT"] {
			t.Fatal("child changed account or accumulated duplicate Git guards")
		}
	}
}

func TestSessionPreservesEnterpriseTokenEnvironment(t *testing.T) {
	for _, variable := range []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Run(variable, func(t *testing.T) {
			records := setupRooms(t, "team/private")
			if out, err := exec.Command("/usr/bin/git", "-C", records[0].Repo, "remote", "set-url", "origin", "https://work.example/team/private.git").CombinedOutput(); err != nil {
				t.Fatalf("git: %s %v", out, err)
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho '{\"hosts\":{}}'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			inherited := append([]string{}, os.Environ()...)
			inherited = append(inherited, "GH_TOKEN=public-token", "GITHUB_TOKEN=other-public-token", "GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN=", variable+"=enterprise-token")
			env, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", inherited)
			if err != nil {
				t.Fatal(err)
			}
			if env["GH_ENTERPRISE_TOKEN"] != "enterprise-token" || env["GH_HOST"] != "work.example" {
				t.Fatal("enterprise owner authentication was lost")
			}
		})
	}
}
