package podman

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

// stackReply answers a command that holds match, times times.
type stackReply struct {
	match, body   string
	status, times int
}

// quoteRunner records each framed command and answers it from replies, in order, or with success.
type quoteRunner struct {
	replies  []stackReply
	commands []string
}

func (r *quoteRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") {
		return nil, nil
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	for i := range r.replies {
		reply := &r.replies[i]
		if reply.times > 0 && strings.Contains(command, reply.match) {
			reply.times--
			return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", reply.body, len(reply.body), reply.status)), nil
		}
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func (r *quoteRunner) index(part string) int {
	return slices.IndexFunc(r.commands, func(command string) bool { return strings.Contains(command, part) })
}

func (r *quoteRunner) count(part string) int {
	n := 0
	for _, command := range r.commands {
		if strings.Contains(command, part) {
			n++
		}
	}
	return n
}

func (r *quoteRunner) containing(part string) string {
	if i := r.index(part); i >= 0 {
		return r.commands[i]
	}
	return ""
}

func argHas(t *testing.T, args []string, want ...string) {
	t.Helper()
	for _, flag := range want {
		if !slices.Contains(args, flag) {
			t.Errorf("no %q in %q", flag, args)
		}
	}
}

func TestPodArgsNameTheNetworkAndShareOnlyIt(t *testing.T) {
	args := podArgs("box_1", "dyna-box_1.slice", &box.Stack{Infra: "dyna-base:1", Names: []string{"agent", "db"}})
	argHas(t, args, "--name", "dyna-pod-box_1", boxUserNS, "--cgroup-parent=dyna-box_1.slice", "--share-parent=false", "--share=net", "--network=pasta",
		"--infra-image=dyna-base:1", "--infra-command=/usr/bin/tini -- sleep infinity", "agent:127.0.0.1", "db:127.0.0.1")
}

func TestSidecarArgsCarryOnlyWhatTheFileAllows(t *testing.T) {
	args := sidecarArgs("box_1", "dyna-box_1.slice", 2, box.Sidecar{
		Name: "db", Image: "docker.io/library/postgres:16", Command: []string{"postgres", "-c", "fsync=off"},
		Env: []box.EnvVar{{Name: "A", Value: "b c"}}, Volumes: []box.Volume{{Name: "data", Target: "/data"}, {Name: "seed", Target: "/seed", ReadOnly: true}},
		Health: []string{"CMD-SHELL", "pg_isready"}, HealthTimeout: 5 * time.Second, Wait: 30 * time.Second, CPUs: 1.5, MemBytes: 1 << 30, Pids: 256,
	})
	argHas(t, args, "--pod", "dyna-pod-box_1", "--cgroup-parent=dyna-box_1.slice", "--pull=never", "dyna-sidecar-box_1-db", "A=b c", "dyna-box-box_1-data:/data", "dyna-box-box_1-seed:/seed:ro",
		"--cpus=1.5", "--memory=1073741824", "--pids-limit=256", `--health-cmd=["CMD-SHELL","pg_isready"]`, "--health-interval=disable", "--health-timeout=5s",
		stackLabel+"=box_1", orderLabel+"=2", waitLabel+"=30")
	if got := strings.Join(args[len(args)-4:], " "); got != "docker.io/library/postgres:16 postgres -c fsync=off" {
		t.Fatalf("the command must follow the image: %q", got)
	}
	// A pod member gets none of what a box gets.
	for _, flag := range args {
		if strings.HasPrefix(flag, "--user") || strings.HasPrefix(flag, "--device") || strings.HasPrefix(flag, "--security-opt") || strings.Contains(flag, "/run/dyna") {
			t.Errorf("sidecar flag %q", flag)
		}
	}
	if slices.Contains(args, "dyna.box=box_1") {
		t.Error("a sidecar must not read as a box")
	}
}

func TestHealthCommandSuitsPodmansParser(t *testing.T) {
	for _, tc := range []struct {
		test []string
		want string
	}{
		{[]string{"CMD-SHELL", "pg_isready -U a"}, `["CMD-SHELL","pg_isready -U a"]`},
		{[]string{"CMD", "redis-cli", "ping"}, `["redis-cli","ping"]`},
	} {
		if got := healthCommand(tc.test); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}

func TestStackBoxJoinsThePod(t *testing.T) {
	r := &quoteRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	spec := box.ContainerSpec{Box: "box_1", Image: "image", Slice: "dyna-box_1.slice", Pod: "dyna-pod-box_1", Volumes: []box.Volume{{Name: "shared", Target: "/shared"}}}
	if err := h.CreateContainer(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	created := r.containing("'create'")
	if !strings.Contains(created, "'--pod' 'dyna-pod-box_1'") || strings.Contains(created, "--userns") || !strings.Contains(created, "'dyna-box-box_1-shared:/shared'") {
		t.Fatalf("create: %s", created)
	}
}

func TestSidecarsStartInOrderAndWaitForHealth(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{
		{match: "'ps' '--all'", body: `[
			{"Names":["second"],"Labels":{"dyna.stack.order":"1","dyna.stack.wait":"0"},"State":"created"},
			{"Names":["box-infra"],"Labels":{},"State":"running"},
			{"Names":["first"],"Labels":{"dyna.stack.order":"0","dyna.stack.wait":"30"},"State":"created"}]`, times: 1},
		{match: "'healthcheck' 'run' 'first'", status: 1, times: 1},
		{match: "'ps' '--all'", body: `[{"Names":["first"],"Labels":{"dyna.stack.order":"0"},"State":"running"},{"Names":["second"],"Labels":{"dyna.stack.order":"1"},"State":"running"}]`, times: 1},
	}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StartSidecars(context.Background(), "box_1"); err != nil {
		t.Fatal(err)
	}
	first, second := r.index("'start' 'first'"), r.index("'start' 'second'")
	probes := r.count("'healthcheck' 'run' 'first'")
	if first < 0 || second < first || probes != 2 || r.index("'healthcheck' 'run' 'second'") >= 0 {
		t.Fatalf("first=%d second=%d probes=%d: %v", first, second, probes, r.commands)
	}
	if r.index("'healthcheck'") > second {
		t.Fatal("the next sidecar started before the first was healthy")
	}
}

func TestSidecarThatDiedIsAnError(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{
		{match: "'ps' '--all'", body: `[{"Names":["db"],"Labels":{"dyna.stack.order":"0","dyna.stack.wait":"0"},"State":"created"}]`, times: 1},
		{match: "'ps' '--all'", body: `[{"Names":["db"],"Labels":{"dyna.stack.order":"0"},"State":"exited","ExitCode":3}]`, times: 1},
	}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	err = h.StartSidecars(context.Background(), "box_1")
	var stack box.StackError
	if !errors.As(err, &stack) || !strings.Contains(err.Error(), "sidecar db is not running: exited, exit code 3") {
		t.Fatalf("got %v", err)
	}
}

func TestAFailedPodSaysWhy(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{{match: "'pod' 'create'", body: "Failed to set up tap device in namespace\n", status: 125, times: 1}}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	err = h.CreateStack(context.Background(), "box_1", "dyna-box_1.slice", &box.Stack{Infra: "base", Sidecars: []box.Sidecar{{Name: "db", Image: "image"}}})
	var stack box.StackError
	if !errors.As(err, &stack) || !strings.Contains(err.Error(), "create pod: Failed to set up tap device") {
		t.Fatalf("got %v", err)
	}
}

func TestUnhealthySidecarTimesOut(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{{match: "'healthcheck'", status: 1, times: 1 << 30}}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	err = h.awaitHealthy(context.Background(), "db", time.Millisecond)
	var stack box.StackError
	if !errors.As(err, &stack) || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("got %v", err)
	}
}

func TestPullUsesNoSignInAndWarms(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{{match: "'image' 'exists'", status: 1, times: 1}}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PullImage(context.Background(), "docker.io/library/postgres:16"); err != nil {
		t.Fatal(err)
	}
	pull := r.index("podman pull")
	warm := r.index("podman create --replace")
	if pull < 0 || warm < pull || !strings.Contains(r.commands[pull], `--authfile=<(printf`) {
		t.Fatalf("pull=%d warm=%d: %v", pull, warm, r.commands)
	}
}

func TestPullSkipsAnImageTheHostHas(t *testing.T) {
	r := &quoteRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PullImage(context.Background(), "docker.io/library/postgres:16"); err != nil {
		t.Fatal(err)
	}
	if r.index("podman pull") >= 0 {
		t.Fatalf("pulled again: %v", r.commands)
	}
}

func TestListContainersReadsTheStackHash(t *testing.T) {
	r := &quoteRunner{replies: []stackReply{{match: "'ps' '--all'", body: `[{"Labels":{"dyna.box":"box_1","dyna.stack.hash":"abc"},"State":"running"}]`, times: 1}}}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := h.ListContainers(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Stack != "abc" {
		t.Fatalf("%+v %v", statuses, err)
	}
}
