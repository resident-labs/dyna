package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func serveRoomCredential(t *testing.T, rec room.Record) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	bindings, err := websvc.Bind([]websvc.Binding{{Name: "GitHubCredential", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true,
		Method: func(ctx context.Context, host, name string) (ghclient.Credential, error) {
			calls.Add(1)
			return ghclient.RoomCredential(ctx, rec, host, name)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bindings)
	return &calls
}

func TestWorkerCredentialsStayInsideDyna(t *testing.T) {
	var pushAuthenticated atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, token, ok := r.BasicAuth()
		if ok && login == "worker" && token == "worker-secret" {
			pushAuthenticated.Store(true)
		}
		w.Header().Set("WWW-Authenticate", "Basic realm=git")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	roomURL := server.URL + "/work/service.git"

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EXPECT_WORKER_GH", "")
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "worker-credential"})
	if err != nil {
		t.Fatal(err)
	}
	rec = roomfixture.Repository(t, room.DefaultStore(), rec.ID, host, "work/service", "read-write")
	bin := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"%s":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"pr view") [ "$EXPECT_WORKER_GH" = 1 ] && [ -z "$GH_TOKEN" ] && exit 90; echo viewed;;
esac
`, host)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	scratch := t.TempDir()
	session, err := ghclient.SessionEnvironment(context.Background(), rec.ID, scratch, binPath, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		if session.Env[key] != "" {
			t.Fatalf("worker environment exposes %s", key)
		}
	}
	config := session.Env["GH_CONFIG_DIR"]
	if config == "" {
		t.Fatal("worker gh has no empty configuration directory")
	}
	if _, err := os.Stat(filepath.Join(config, "hosts.yml")); !os.IsNotExist(err) {
		t.Fatal("worker gh can read a saved account")
	}
	credentialCalls := serveRoomCredential(t, rec)
	env := append([]string{}, os.Environ()...)
	for key, value := range session.Env {
		env = append(env, key+"="+value)
	}
	env = append(env, mode.EnvRoomID+"="+rec.ID, "EXPECT_WORKER_GH=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	credential := func(url string) (string, error) {
		cmd := exec.Command("/usr/bin/git", "credential", "fill")
		cmd.Dir = rec.Repo
		cmd.Env = env
		cmd.Stdin = strings.NewReader("url=" + url + "\n\n")
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := credential(roomURL)
	if err != nil {
		t.Fatalf("room git credential failed: %v", err)
	}
	if !strings.Contains(out, "username=worker\n") || !strings.Contains(out, "password=worker-secret\n") {
		t.Fatal("room git credential did not receive the selected account")
	}

	guarded := exec.Command(filepath.Join(scratch, "github-bin", "gh"), "pr", "view", "1")
	guarded.Dir = rec.Repo
	guarded.Env = env
	if _, err := guarded.CombinedOutput(); err != nil {
		t.Fatalf("guarded gh could not use the internal account: %v", err)
	}

	commit := exec.Command("/usr/bin/git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-q", "-m", "initial")
	commit.Dir = rec.Repo
	if err := commit.Run(); err != nil {
		t.Fatalf("create push commit: %v", err)
	}
	push := exec.Command("/usr/bin/git", "-c", "http.sslVerify=false", "push", "origin", "HEAD:main")
	push.Dir = rec.Repo
	push.Env = env
	_ = push.Run()
	if !pushAuthenticated.Load() {
		t.Fatal("git push did not receive the room credential")
	}

	beforeRefused := credentialCalls.Load()
	direct := exec.Command(binPath, "git-credential", "get")
	direct.Env = env
	direct.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\npath=work/service.git\n\n")
	output, err := direct.CombinedOutput()
	if err != nil || len(output) != 0 {
		t.Fatalf("non-git caller received a response or error: %v", err)
	}

	wrongEnv := append(env, "GIT_CONFIG_COUNT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	wrong := exec.Command("/usr/bin/git", "-c", "credential.helper=", "-c", "credential.helper=!"+shellquote.Quote(binPath)+" git-credential", "credential", "fill")
	wrong.Dir = rec.Repo
	wrong.Env = wrongEnv
	wrong.Stdin = strings.NewReader("url=" + server.URL + "/other/private.git\n\n")
	output, err = wrong.Output()
	if err == nil || strings.Contains(string(output), "password=") {
		t.Fatal("wrong repository received a credential")
	}
	if credentialCalls.Load() != beforeRefused {
		t.Fatal("a refused caller reached the engine credential")
	}
}
