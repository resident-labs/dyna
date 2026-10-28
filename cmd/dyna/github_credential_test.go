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
	var roomPushes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, token, ok := r.BasicAuth()
		if ok && login == "worker" && token == "worker-secret" {
			if strings.HasPrefix(r.URL.Path, "/other/") {
				wrongRepoAuthenticated.Store(true)
			} else {
				pushAuthenticated.Store(true)
				roomPushes.Add(1)
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

	// A repository push-only URL rewrite must not turn an engine push into an
	// external transport that carries the account token in its environment.
	// pushInsteadOf leaves the fetch URL intact, so origin checks alone do not
	// see it; only rewrite rejection and the transport lock stop it.
	marker := filepath.Join(t.TempDir(), "ext-env")
	rewrite := "ext::sh -c env>" + marker + " #"
	for _, args := range [][]string{
		{"config", "url." + rewrite + ".pushInsteadOf", server.URL},
		{"config", "protocol.ext.allow", "always"},
	} {
		if out, err := exec.Command("/usr/bin/git", append([]string{"-C", rec.Repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	rewritten := exec.Command(guardedGit, "push", roomURL, "HEAD:main")
	rewritten.Dir = rec.Repo
	rewritten.Env = env
	_ = rewritten.Run()
	leaked, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal("the repository rewrite never engaged")
	}
	if strings.Contains(string(leaked), "worker-secret") {
		t.Fatal("an engine push ran through a repository rewrite with the account token")
	}
	// The push-only rewrite must not poison later checks of this repository.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "--remove-section", "url."+rewrite).CombinedOutput(); err != nil {
		t.Fatalf("remove rewrite section: %v %s", err, out)
	}

	// A push that names another repository directory (-C) must not carry the
	// engine credential into that repository's rewritten URL and helper.
	controlled := filepath.Join(t.TempDir(), "controlled")
	for _, args := range [][]string{
		{"init", "-q", controlled},
		{"-C", controlled, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "x"},
	} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	attackMarker := filepath.Join(t.TempDir(), "helper-env")
	helper := "!sh -c env>" + attackMarker + " #"
	for _, args := range [][]string{
		{"-C", controlled, "config", "url." + roomURL + "/x.insteadOf", roomURL},
		{"-C", controlled, "config", "credential.https://" + strings.TrimPrefix(roomURL, "https://") + "/x.helper", helper},
	} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	redirected := exec.Command(guardedGit, "-C", controlled, "push", roomURL, "HEAD:main")
	redirected.Dir = rec.Repo
	// The local fallback must reach the test server, so it trusts the fixture CA.
	redirected.Env = append(append([]string{}, env...), "GIT_SSL_CAINFO="+certificate)
	_ = redirected.Run()
	leak, err := os.ReadFile(attackMarker)
	if err != nil {
		t.Fatal("the controlled repository helper never engaged")
	}
	if strings.Contains(string(leak), "worker-secret") {
		t.Fatal("the engine token reached a controlled repository credential helper")
	}

	// push.default=nothing must reject a push without refspecs locally, not
	// send the current branch with engine credentials.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "push.default", "nothing").CombinedOutput(); err != nil {
		t.Fatalf("set push.default: %v %s", err, out)
	}
	pushes := roomPushes.Load()
	rejected := exec.Command(guardedGit, "push", "origin")
	rejected.Dir = rec.Repo
	rejected.Env = env
	_ = rejected.Run()
	if roomPushes.Load() != pushes {
		t.Fatal("push.default=nothing reached the remote with engine credentials")
	}
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "--unset", "push.default").CombinedOutput(); err != nil {
		t.Fatalf("unset push.default: %v %s", err, out)
	}

	// --force-if-includes changes what a lease protects, and the engine
	// resolves leases itself, so this form keeps its exact behavior locally.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "update-ref", "refs/remotes/origin/main", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("set tracking ref: %v %s", err, out)
	}
	pushes = roomPushes.Load()
	leased := exec.Command(guardedGit, "push", "--force-with-lease=refs/heads/main", "--force-if-includes", "origin", "HEAD:main")
	leased.Dir = rec.Repo
	leased.Env = env
	_ = leased.Run()
	if roomPushes.Load() != pushes {
		t.Fatal("--force-if-includes reached the remote with engine credentials")
	}

	// A branch without an upstream and no push.autoSetupRemote must stay
	// local: git itself rejects the push without touching the network.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "checkout", "-q", "-b", "topic").CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v %s", err, out)
	}
	pushes = roomPushes.Load()
	noUpstream := exec.Command(guardedGit, "push", "origin")
	noUpstream.Dir = rec.Repo
	noUpstream.Env = env
	_ = noUpstream.Run()
	if roomPushes.Load() != pushes {
		t.Fatal("a simple push without an upstream reached the remote with engine credentials")
	}

	// push.autoSetupRemote turns that push into an engine push, as git
	// accepts it, and records the new upstream in the worker repository.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "push.autoSetupRemote", "true").CombinedOutput(); err != nil {
		t.Fatalf("set autoSetupRemote: %v %s", err, out)
	}
	pushes = roomPushes.Load()
	setup := exec.Command(guardedGit, "push", "origin")
	setup.Dir = rec.Repo
	setup.Env = env
	_ = setup.Run()
	if roomPushes.Load() == pushes {
		t.Fatal("push.autoSetupRemote did not push the branch through the engine")
	}
	remote, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "--get", "branch.topic.remote").Output()
	if err == nil {
		t.Fatalf("a failed push set an upstream: %s", remote)
	}

	// A branch that tracks one remote pushes to another remote under simple
	// mode: git pushes the branch to its own name and keeps the upstream,
	// with or without push.autoSetupRemote.
	if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "remote", "add", "fork", roomURL).CombinedOutput(); err != nil {
		t.Fatalf("add fork remote: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"config", "branch.topic.remote", "origin"},
		{"config", "branch.topic.merge", "refs/heads/topic"},
		{"config", "--unset", "push.autoSetupRemote"},
	} {
		if out, err := exec.Command("/usr/bin/git", append([]string{"-C", rec.Repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	for _, auto := range []string{"false", "true"} {
		if auto == "true" {
			if out, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "push.autoSetupRemote", "true").CombinedOutput(); err != nil {
				t.Fatalf("set autoSetupRemote: %v %s", err, out)
			}
		}
		pushes = roomPushes.Load()
		second := exec.Command(guardedGit, "push", "fork")
		second.Dir = rec.Repo
		second.Env = env
		_ = second.Run()
		if roomPushes.Load() == pushes {
			t.Fatalf("push to a second remote (autoSetupRemote=%s) did not use the engine", auto)
		}
		remote, err := exec.Command("/usr/bin/git", "-C", rec.Repo, "config", "--get", "branch.topic.remote").Output()
		if err != nil || strings.TrimSpace(string(remote)) != "origin" {
			t.Fatalf("push to a second remote (autoSetupRemote=%s) changed the tracked remote: %v %s", auto, err, remote)
		}
	}
}
