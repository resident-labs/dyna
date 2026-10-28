package main

import (
	"context"
	"encoding/pem"
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
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

type roomOperationCalls struct {
	github atomic.Int32
	push   atomic.Int32
}

func (c *roomOperationCalls) total() int32 {
	return c.github.Load() + c.push.Load()
}

func serveRoomOperations(t *testing.T, rec room.Record) *roomOperationCalls {
	t.Helper()
	calls := &roomOperationCalls{}
	bindings, err := websvc.Bind([]websvc.Binding{
		{Name: "GitHubCommand", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true,
			Method: func(ctx context.Context, request ghclient.CommandRequest) (ghclient.CommandResult, error) {
				calls.github.Add(1)
				return ghclient.RunSessionGitHub(ctx, rec, request)
			}},
		{Name: "GitPush", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true,
			Method: func(ctx context.Context, request ghclient.CommandRequest) (ghclient.CommandResult, error) {
				calls.push.Add(1)
				return ghclient.RunSessionGitPush(ctx, rec, request)
			}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bindings)
	return calls
}

func TestWorkerCredentialsStayInsideDyna(t *testing.T) {
	var pushAuthenticated, wrongRepoAuthenticated atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, token, ok := r.BasicAuth()
		if ok && login == "worker" && token == "worker-secret" {
			if strings.HasPrefix(r.URL.Path, "/other/") {
				wrongRepoAuthenticated.Store(true)
			} else {
				pushAuthenticated.Store(true)
			}
		}
		w.Header().Set("WWW-Authenticate", "Basic realm=git")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.PushCAEnv, certificate)
	host := strings.TrimPrefix(server.URL, "https://")
	roomURL := server.URL + "/work/service.git"

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
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
"auth git-credential") h=; while IFS= read -r line; do case "$line" in host=*) h=${line#host=};; esac; done; printf 'protocol=https
host=%%s
username=worker
password=worker-secret
' "$h";;
"api --hostname") echo '{"permissions":{"push":true}}';;
"pr view") echo viewed;;
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
	operations := serveRoomOperations(t, rec)
	env := append([]string{}, os.Environ()...)
	for key, value := range session.Env {
		env = append(env, key+"="+value)
	}
	env = append(env, mode.EnvRoomID+"="+rec.ID, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	guardedGit := filepath.Join(scratch, "github-bin", "git")

	beforeCredentials := operations.total()
	for _, target := range []string{roomURL, server.URL + "/other/private.git"} {
		credential := exec.Command(guardedGit, "credential", "fill")
		credential.Dir = rec.Repo
		credential.Env = env
		credential.Stdin = strings.NewReader("url=" + target + "\n\n")
		output, err := credential.Output()
		if err == nil || strings.Contains(string(output), "password=") {
			t.Fatal("worker read a credential through git")
		}
	}
	if operations.total() != beforeCredentials {
		t.Fatal("a credential read reached an engine operation")
	}

	guarded := exec.Command(filepath.Join(scratch, "github-bin", "gh"), "pr", "view", "1")
	guarded.Dir = rec.Repo
	guarded.Env = env
	output, err := guarded.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "viewed") {
		t.Fatalf("guarded gh could not use the engine account: %v", err)
	}
	if operations.github.Load() != 1 {
		t.Fatal("guarded gh did not use the engine operation")
	}

	commit := exec.Command("/usr/bin/git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-q", "-m", "initial")
	commit.Dir = rec.Repo
	if err := commit.Run(); err != nil {
		t.Fatalf("create push commit: %v", err)
	}
	push := exec.Command(guardedGit, "push", "origin", "HEAD:main")
	push.Dir = rec.Repo
	push.Env = env
	_ = push.Run()
	if !pushAuthenticated.Load() || operations.push.Load() != 1 {
		t.Fatalf("engine git push did not receive the room credential (github=%d push=%d)", operations.github.Load(), operations.push.Load())
	}

	wrongPush := exec.Command(guardedGit, "push", server.URL+"/other/private.git", "HEAD:main")
	wrongPush.Dir = rec.Repo
	wrongPush.Env = env
	_ = wrongPush.Run()
	if wrongRepoAuthenticated.Load() {
		t.Fatal("wrong repository received the owner credential")
	}
}
