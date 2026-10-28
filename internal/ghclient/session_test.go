package ghclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
)

// installFakeGH isolates executable lookup from the live session environment.
func installFakeGH(t *testing.T, script string) string {
	t.Helper()
	t.Setenv(BinaryEnv, "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return bin
}

func gitConfigValue(env map[string]string, key string) string {
	count, _ := strconv.Atoi(env["GIT_CONFIG_COUNT"])
	for i := 0; i < count; i++ {
		if env["GIT_CONFIG_KEY_"+strconv.Itoa(i)] == key {
			return env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
		}
	}
	return ""
}

func TestSessionUsesTheRoomAccount(t *testing.T) {
	records := setupRooms(t, "resident-labs/personal", "work/private")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_REPO", "unscoped/other")
	bin := installFakeGH(t, `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"personal"},{"login":"work"}]}}';;
"auth token") case "$*" in *personal*) echo personal-secret;; *work*) echo work-secret;; esac;;
"api --hostname") case "$GH_TOKEN:$4" in personal-secret:repos/resident-labs/personal|work-secret:repos/work/private) echo '{"permissions":{"push":true}}';; *) echo "gh: Not Found (HTTP 404)" >&2; exit 1;; esac;;
esac
`)
	for i, rec := range records {
		scratch := t.TempDir()
		session, err := SessionEnvironment(context.Background(), rec.ID, scratch, "/real/dyna", os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		env := session.Env
		want := []string{"personal-secret", "work-secret"}[i]
		if env["GH_TOKEN"] != want || env["GH_HOST"] != "github.com" || env["GH_REPO"] != "" {
			t.Fatal("session has the wrong repository credentials")
		}
		target, err := os.Readlink(filepath.Join(scratch, "github-bin", "gh"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (target != "/real/dyna" || env[ReadOnlyEnv] != "worker") {
			t.Fatal("write room did not receive the worker gh guard")
		}
		if i == 1 && (target != "/real/dyna" || env[ReadOnlyEnv] != "1") {
			t.Fatal("read-only room did not receive the gh guard")
		}
		helperKey := "credential.https://github.com/" + []string{"resident-labs/personal", "work/private"}[i] + ".helper"
		helper := gitConfigValue(env, helperKey)
		if i == 0 && helper != "!"+shellquote.Quote(filepath.Join(bin, "gh"))+" auth git-credential" {
			t.Fatalf("write room git has no gh credential helper: %q", helper)
		}
		if i == 1 && helper != "" {
			t.Fatal("read-only room git received a push credential helper")
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
		if child.Env["GH_TOKEN"] != want || child.Env["GIT_CONFIG_COUNT"] != env["GIT_CONFIG_COUNT"] {
			t.Fatal("child changed account or accumulated duplicate Git guards")
		}
	}
}

func TestWriteRoomSessionGuard(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_SESSION_CALLS", calls)
	installFakeGH(t, `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo '{"permissions":{"push":true}}';;
*) printf '%s\n' "$*" >> "$GH_SESSION_CALLS";;
esac
`)
	scratch := t.TempDir()
	session, err := SessionEnvironment(context.Background(), records[0].ID, scratch, "/real/dyna", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range session.Env {
		t.Setenv(key, value)
	}
	t.Setenv("DYNA_ROOM_ID", records[0].ID)
	t.Chdir(records[0].Repo)
	for _, args := range [][]string{
		{"pr", "comment", "1", "--body", "not allowed"},
		{"api", "repos/work/service/issues", "-X", "POST"},
	} {
		if err := SessionCommand(context.Background(), args); err == nil || !strings.Contains(err.Error(), "post in the room with dyna post instead") {
			t.Fatalf("guard allowed %q: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"pr", "create", "--title", "test", "--body", "test"},
		{"pr", "edit", "1", "--title", "test"},
		{"pr", "ready", "1"},
		{"pr", "view", "1"},
	} {
		if err := SessionCommand(context.Background(), args); err != nil {
			t.Fatalf("guard refused %q: %v", args, err)
		}
	}
	target, err := os.Readlink(filepath.Join(scratch, "github-bin", "gh"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/real/dyna" || session.Env[ReadOnlyEnv] != "worker" {
		t.Fatalf("write room guard target=%q mode=%q", target, session.Env[ReadOnlyEnv])
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pr comment") || strings.Contains(string(raw), "api repos/work/service/issues -X POST") {
		t.Fatalf("refused command reached gh: %s", raw)
	}
}

func TestSessionRunsWithoutTheGitHubCLI(t *testing.T) {
	records := setupRooms(t, "work/service")
	// An empty PATH has no gh, so a turn in a GitHub repository must still run.
	t.Setenv("PATH", t.TempDir())
	for _, access := range []string{ReadWrite, ReadOnly} {
		t.Run(access, func(t *testing.T) {
			if err := SetAccess(room.DefaultStore(), records[0].ID, access); err != nil {
				t.Fatal(err)
			}
			scratch := t.TempDir()
			session, err := SessionEnvironment(context.Background(), records[0].ID, scratch, "/real/dyna", os.Environ())
			if err != nil {
				t.Fatalf("a missing GitHub CLI failed the turn: %v", err)
			}
			if !strings.Contains(session.Notice, "not installed") {
				t.Fatalf("notice = %q, want the missing CLI line", session.Notice)
			}
			target, err := os.Readlink(filepath.Join(scratch, "github-bin", "gh"))
			if err != nil {
				t.Fatal(err)
			}
			wantMode := "worker"
			if access == ReadOnly {
				wantMode = "1"
			}
			if session.Env[ReadOnlyEnv] != wantMode || target != "/real/dyna" {
				t.Fatalf("guard mode=%q target=%q, want mode=%q and the dyna binary", session.Env[ReadOnlyEnv], target, wantMode)
			}
			if helper := gitConfigValue(session.Env, "credential.https://github.com/work/service.helper"); helper != "" {
				t.Fatalf("git received a credential helper: %q", helper)
			}
		})
	}
}

func TestSessionPreservesEnterpriseTokenEnvironment(t *testing.T) {
	for _, variable := range []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Run(variable, func(t *testing.T) {
			records := setupRooms(t, "team/private")
			if out, err := exec.Command("/usr/bin/git", "-C", records[0].Repo, "remote", "set-url", "origin", "https://work.example/team/private.git").CombinedOutput(); err != nil {
				t.Fatalf("git: %s %v", out, err)
			}
			installFakeGH(t, "#!/bin/sh\necho '{\"hosts\":{}}'\n")
			inherited := append([]string{}, os.Environ()...)
			inherited = append(inherited, "GH_TOKEN=public-token", "GITHUB_TOKEN=other-public-token", "GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN=", variable+"=enterprise-token")
			env, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", inherited)
			if err != nil {
				t.Fatal(err)
			}
			if env.Env["GH_ENTERPRISE_TOKEN"] != "enterprise-token" || env.Env["GH_HOST"] != "work.example" {
				t.Fatal("enterprise owner authentication was lost")
			}
		})
	}
}
