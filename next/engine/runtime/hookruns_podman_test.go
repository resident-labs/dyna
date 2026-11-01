package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	enginegit "github.com/resident-labs/dyna/engine/git"
)

// sshForwards runs host commands here and stands in for ssh -R with a small proxy, so a box's socket
// reaches the engine's as it does across a real control master.
type sshForwards struct {
	mu        sync.Mutex
	listeners map[string]net.Listener
}

func (f *sshForwards) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") {
		if i := slices.Index(args, "-R"); i >= 0 {
			host, engine, _ := strings.Cut(args[i+1], ":")
			if slices.Contains(args, "cancel") {
				f.close(host)
				return nil, nil
			}
			return nil, f.forward(host, engine)
		}
		return nil, nil
	}
	if slices.Contains(args, "-M") {
		return nil, nil
	}
	c := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	c.Stdin = bytes.NewReader(input)
	return c.Output()
}

func (f *sshForwards) forward(host, engine string) error {
	l, err := net.Listen("unix", host)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.listeners == nil {
		f.listeners = make(map[string]net.Listener)
	}
	f.listeners[host] = l
	f.mu.Unlock()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				target, err := net.Dial("unix", engine)
				if err != nil {
					return
				}
				defer target.Close()
				go func() { _, _ = io.Copy(target, conn) }()
				_, _ = io.Copy(conn, target)
			}()
		}
	}()
	return nil
}

func (f *sshForwards) close(host string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.listeners[host]; l != nil {
		_ = l.Close()
		delete(f.listeners, host)
	}
}

type hookPosts struct {
	api.Service
	mu    sync.Mutex
	posts []contract.PostRequest
	sub   contract.SubscriptionID
	room  contract.RoomID
}

func (h *hookPosts) HookPost(_ context.Context, sub contract.SubscriptionID, room contract.RoomID, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posts, h.sub, h.room = append(h.posts, req), sub, room
	return contract.PostResponse{Event: "ev_box", Seq: 1}, nil
}

const realRunScript = `#!/bin/bash
set -eu
echo "started as uid $(id -u) in $PWD"
test -z "${GH_TOKEN:-}"
! grep -qs token-held-by-the-engine /proc/self/environ
! touch /box/hook/new 2>/dev/null
! touch /usr/new 2>/dev/null
touch /box/home/scratch /tmp/scratch
test -x ./watch.sh && test -f sub/data.txt
n=$(cat /box/shared/count 2>/dev/null || echo 0)
echo $((n + 1)) > /box/shared/count
curl -fsS --unix-socket "$DYNA_SOCKET" http://api.github.com/repos/o/r/issues | grep -q curl-title
mkdir -p /tmp/gh
printf 'http_unix_socket: %s\n' "$DYNA_SOCKET" > /tmp/gh/config.yml
echo "gh read: $(GH_CONFIG_DIR=/tmp/gh GH_TOKEN=placeholder gh api repos/o/r/issues --jq '.[0].title')"
code=$(curl -s -o /dev/null -w '%{http_code}' --unix-socket "$DYNA_SOCKET" -X POST http://api.github.com/graphql -d '{}')
test "$code" = 403
code=$(curl -s -o /dev/null -w '%{http_code}' --unix-socket "$DYNA_SOCKET" -X POST http://api.github.com/repos/o/r/merges -d '{}')
test "$code" = 403
code=$(curl -s -o /dev/null -w '%{http_code}' --unix-socket "$DYNA_SOCKET" -H 'Content-Type: application/json' -d '{}' http://localhost/v1/room.list)
test "$code" = 403
curl -fsS --unix-socket "$DYNA_SOCKET" -H 'Content-Type: application/json' -d '{"text":"new upstream commits","mentions":["review-lead"]}' http://localhost/v1/post
echo
echo "count $(cat /box/shared/count)" >&2
`

// A Run in a real small box on real Podman, through the real socket route and the real gh. DYNA_PODMAN_TEST_RUN_IMAGE
// names a small local image with /usr/bin/tini, bash, curl and gh: keep-id chowns a whole image on first use.
func TestRealRunInASmallBox(t *testing.T) {
	image := os.Getenv("DYNA_PODMAN_TEST_RUN_IMAGE")
	if image == "" {
		t.Skip("DYNA_PODMAN_TEST_RUN_IMAGE names a local image; this test runs real podman")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var mu sync.Mutex
	var headers []http.Header
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"title":"curl-title"}]`)
	}))
	defer github.Close()
	target, err := url.Parse(github.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := &hookPosts{}
	server := api.New(service, nil)
	server.SetGitHubRead(enginegit.ReadOnly(target, http.DefaultTransport, "token-held-by-the-engine"))
	data, err := os.MkdirTemp("", "dyna-run-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	runtimeDir := filepath.Join(data, "rt")
	sockets := api.NewSockets(server, runtimeDir, uint32(os.Getuid()), nil)
	defer sockets.Close()
	host, err := podman.New("local", runtimeDir, "h", &sshForwards{})
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Layout: layout, Runtime: runtimeDir, Sockets: sockets, hosts: map[contract.HostName]*podman.Host{"h": host}}
	size, _ := box.Size(contract.BoxSizeSmall)
	spec := func(id contract.BoxID, command ...string) box.RunSpec {
		return box.RunSpec{ID: id, Subscription: "sub_watch", Host: "h", Room: "room_watch", Size: size, Command: command,
			Files: []contract.RunFile{{Path: "watch.sh", Body: realRunScript}, {Path: "sub/data.txt", Body: "data"}}}
	}
	wait := func(id contract.BoxID) box.RunStatus {
		t.Helper()
		for {
			status, err := b.RunStatus(ctx, "h", id)
			if err != nil {
				t.Fatal(err)
			}
			if status.Found && !status.Running {
				return status
			}
			if ctx.Err() != nil {
				t.Fatalf("run %s did not end: %+v", id, status)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	run := func(id contract.BoxID, command ...string) (box.RunStatus, string) {
		t.Helper()
		if err := b.boot(ctx, host, spec(id, command...), image); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := b.RemoveRun(context.Background(), "h", id); err != nil {
				t.Error(err)
			}
		}()
		status := wait(id)
		out, err := b.RunOutput(ctx, "h", id)
		if err != nil {
			t.Fatal(err)
		}
		return status, string(out)
	}
	stamp := time.Now().UnixNano()
	first := contract.BoxID(fmt.Sprintf("run_real_a%d", stamp))
	status, out := run(first, "./watch.sh")
	if status.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", status.ExitCode, out)
	}
	if !strings.Contains(out, "gh read: curl-title") || !strings.Contains(out, "count 1") || !strings.Contains(out, "uid 1000 in /box/hook") {
		t.Fatalf("output:\n%s", out)
	}
	if len(service.posts) != 1 || service.posts[0].Text != "new upstream commits" || service.sub != "sub_watch" || service.room != "room_watch" {
		t.Fatalf("post: %+v %s %s", service.posts, service.sub, service.room)
	}
	// /box/shared is what carries a watcher's state from one Run to the next, and the second box proves it.
	second := contract.BoxID(fmt.Sprintf("run_real_b%d", stamp))
	if status, out = run(second, "./watch.sh"); status.ExitCode != 0 || !strings.Contains(out, "count 2") {
		t.Fatalf("second run, exit %d:\n%s", status.ExitCode, out)
	}
	for _, seen := range headers {
		if seen.Get("Authorization") != "Bearer token-held-by-the-engine" {
			t.Fatalf("GitHub saw %q", seen.Get("Authorization"))
		}
	}
	if len(headers) < 4 {
		t.Fatalf("GitHub saw %d reads", len(headers))
	}
	third := contract.BoxID(fmt.Sprintf("run_real_c%d", stamp))
	if status, out = run(third, "bash", "-c", "echo out; echo err >&2; exit 7"); status.ExitCode != 7 || !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("failing run, exit %d:\n%s", status.ExitCode, out)
	}
	// RemoveRun took everything: no container, no files, no socket.
	if found, err := host.ListRuns(ctx); err != nil || slices.Contains(found, first) || slices.Contains(found, third) {
		t.Fatalf("containers left: %v %v", found, err)
	}
	if _, err := os.Stat(layout.HostRun(first)); !os.IsNotExist(err) {
		t.Fatalf("run folder left: %v", err)
	}
	if _, err := os.Stat(layout.HostSocketFolder(first)); !os.IsNotExist(err) {
		t.Fatalf("socket folder left: %v", err)
	}
	// The limits are what the spec promises for a small box.
	limits := contract.BoxID(fmt.Sprintf("run_real_d%d", stamp))
	if err := b.boot(ctx, host, spec(limits, "sleep", "60"), image); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.RemoveRun(context.Background(), "h", limits) }()
	inspect := podmanCommand(t, "inspect", "--format", "{{.HostConfig.Memory}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.PidsLimit}} {{.HostConfig.CgroupParent}} {{.Config.Timeout}}", string(limits))
	want := fmt.Sprintf("%d true %d dyna-run.slice %d", size.MemBytes, size.Pids, int(box.RunLimit.Seconds()))
	if inspect != want {
		t.Fatalf("inspect = %q, want %q", inspect, want)
	}
}
