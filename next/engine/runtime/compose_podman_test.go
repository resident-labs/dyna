package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/compose"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

const stackFile = `
services:
  agent:
    environment:
      FROM_FILE: agent
    depends_on:
      db:
        condition: service_healthy
    volumes:
      - data:/shared
  db:
    image: %s
    environment:
      GENERATION: "%s"
    command:
      - sh
      - -c
      - "sleep 2; echo up > /data/ready; nc -lk -p 5432 -e /bin/cat"
    volumes:
      - data:/data
    healthcheck:
      test: ["CMD-SHELL", "test -e /data/ready"]
      interval: 2s
      retries: 15
    mem_limit: 512m
  web:
    image: %s
    depends_on: [db]
    command: ["sh", "-c", "nc -lk -p 8080 -e /bin/cat"]
volumes:
  data:
`

// stackStore serves the one box and session the real test drives.
type stackStore struct {
	boxStore
	id      contract.BoxID
	session contract.SessionID
}

func (s stackStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, Session: s.session, Host: "host", State: contract.BoxReady}, nil
}

func (s stackStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{ID: s.session, Room: "room", Box: s.id, Compose: "default"}, nil
}

// standInPasta gives Podman a stand-in where pasta cannot run, as on pve, which has no /dev/net/tun. It only brings up the
// namespace's loopback, which is all that sidecars and a box share. Podman still takes its pasta path, with the flags a
// real host gets, so the shared hosts file and the pod's namespace are real. Only the way out to the internet is missing.
func standInPasta(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		return
	}
	dir := t.TempDir()
	script := "#!/bin/bash\nns=\nwhile [ $# -gt 0 ]; do case \"$1\" in --netns) ns=$2; shift;; esac; shift; done\n[ -z \"$ns\" ] || nsenter --net=\"$ns\" ip link set lo up\n"
	if err := os.WriteFile(filepath.Join(dir, "pasta"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "override.conf")
	body := fmt.Sprintf("[engine]\nhelper_binaries_dir = [%q, \"/usr/lib/podman\", \"/usr/libexec/podman\", \"/usr/bin\"]\n", dir)
	if err := os.WriteFile(conf, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTAINERS_CONF_OVERRIDE", conf)
}

func mappedCopies(t *testing.T, image string) int {
	t.Helper()
	id := podmanCommand(t, "image", "inspect", "--format", "{{.Id}}", image)
	root := podmanCommand(t, "info", "--format", "{{.Store.GraphRoot}}")
	data, err := os.ReadFile(filepath.Join(root, "overlay-images", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images []struct {
		ID     string   `json:"id"`
		Mapped []string `json:"mapped-layers"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	for _, record := range images {
		if record.ID == id {
			return len(record.Mapped)
		}
	}
	t.Fatalf("image %s is not in the store", image)
	return 0
}

// One real stack on real Podman, from a parsed compose file to nothing left behind: a box and two sidecars, reachable from
// the box by loopback and by name, with a healthcheck the box waits for, through dormancy, a swap and teardown.
//
// DYNA_PODMAN_TEST_BASE names a small local image with /usr/bin/tini, nc and sh. DYNA_PODMAN_TEST_SIDECAR names a small
// registry image with nc and sh, which the test removes and pulls again. Use a private store: keep-id copies a whole image
// the first time, and holds that store's lock meanwhile.
func TestComposeStackOnRealPodman(t *testing.T) {
	base, sidecarImage := os.Getenv("DYNA_PODMAN_TEST_BASE"), os.Getenv("DYNA_PODMAN_TEST_SIDECAR")
	if base == "" || sidecarImage == "" {
		t.Skip("DYNA_PODMAN_TEST_BASE and DYNA_PODMAN_TEST_SIDECAR name images; this test runs real podman")
	}
	standInPasta(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	stamp := fmt.Sprintf("%d", time.Now().UnixNano())
	id := contract.BoxID("box_stack_" + stamp)
	_ = exec.Command("podman", "rmi", sidecarImage).Run()

	build := func(name string) string {
		dir := t.TempDir()
		file := fmt.Sprintf("FROM %s\nUSER root\nRUN mkdir -p /box && chown 1000:1000 /box && echo %s > /image-name\n", base, name)
		if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
		tag := "localhost/dyna-stack-test:" + name + stamp
		podmanCommand(t, "build", "-q", "-t", tag, dir)
		t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", tag).Run() })
		return tag
	}
	oldImage, newImage := build("old"), build("new")

	data, err := os.MkdirTemp("", "dyna-stack-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	// This agent notes what it found when it started, then idles.
	agent := filepath.Join(data, "bin", "current", "dyna")
	if err := os.MkdirAll(filepath.Dir(agent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte("#!/bin/sh\ncp /image-name /box/woke-on\ncat /shared/ready > /box/sidecar-was-ready\nexec sleep 300\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "sock", string(id)), 0755); err != nil {
		t.Fatal(err)
	}

	host, err := podman.New("local@host", t.TempDir(), "local", localHost{})
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, installed: map[contract.HostName]bool{"host": true}, Store: stackStore{id: id, session: "ses_stack"}, Runtime: t.TempDir()}
	b.Layout.HostData = data
	slice, err := box.SliceName(id)
	if err != nil {
		t.Fatal(err)
	}
	spec := lifecycle.BoxSpec{Box: id, Room: "room", Host: "host", Slice: slice, Size: contract.BoxSize{CPUs: 1, MemBytes: 1 << 30, Pids: 512}}
	forward := b.forward(id)
	t.Cleanup(func() { _ = removeHostBox(context.Background(), host, box.Box{ID: id, Slice: slice}, forward) })

	stack := func(generation string) *box.Stack {
		t.Helper()
		file, err := compose.Parse(fmt.Appendf(nil, stackFile, sidecarImage, generation, sidecarImage))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.CheckLimits(contract.BoxSize{CPUs: 1, MemBytes: 1 << 30, Pids: 512}); err != nil {
			t.Fatal(err)
		}
		s, err := b.stack(ctx, host, "host", "room", "", base, file)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	exists := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	boxSays := func(want, script string) {
		t.Helper()
		var out []byte
		var err error
		for ctx.Err() == nil {
			out, err = exec.Command("podman", "exec", string(id), "sh", "-c", script).CombinedOutput()
			if err == nil && strings.TrimSpace(string(out)) == want {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: got %q, want %q: %v", script, out, want, err)
	}
	db, web := box.SidecarName(id, "db"), box.SidecarName(id, "web")

	spec.Stack = stack("one")
	// The pulled image comes warm, and the pod's mapping is the one a warm prepares, so nothing copies it twice.
	if copies := mappedCopies(t, spec.Stack.Sidecars[0].Image); copies != 1 {
		t.Fatalf("%d keep-id copies after the pull", copies)
	}
	// A first start makes the same host calls as a new box, minus the socket forward this test has no use for.
	if err := b.Recreate(ctx, spec, oldImage, slice); err != nil {
		t.Fatal(err)
	}
	if copies := mappedCopies(t, spec.Stack.Sidecars[0].Image); copies != 1 {
		t.Fatalf("%d keep-id copies once running", copies)
	}

	t.Log("the box waited for its sidecar and reaches both by loopback and by name")
	boxSays("old", "cat /box/woke-on")
	boxSays("up", "cat /box/sidecar-was-ready")
	boxSays("agent", "echo $FROM_FILE")
	boxSays("hello", "echo hello | nc -w2 127.0.0.1 5432")
	boxSays("hello", "echo hello | nc -w2 db 5432")
	boxSays("hello", "echo hello | nc -w2 web 8080")

	t.Log("sidecars keep the box's user mapping, its slice, and no host mount")
	if got, want := exists("exec", db, "cat", "/proc/self/uid_map"), exists("exec", string(id), "cat", "/proc/self/uid_map"); got != want {
		t.Fatalf("sidecar map %q, box map %q", got, want)
	}
	for _, container := range []string{string(id), db, web} {
		pid := exists("inspect", "--format", "{{.State.Pid}}", container)
		cgroup, err := os.ReadFile("/proc/" + pid + "/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(cgroup), slice) {
			t.Fatalf("%s is outside the slice: %s", container, cgroup)
		}
	}
	if mounts := exists("inspect", "--format", "{{range .Mounts}}{{.Type}} {{end}}", db); strings.Contains(mounts, "bind") {
		t.Fatalf("sidecar has a bind mount: %s", mounts)
	}
	if got := exists("inspect", "--format", "{{.HostConfig.Memory}}", db); got != "536870912" {
		t.Fatalf("sidecar memory %s", got)
	}
	if statuses, err := host.ListContainers(ctx); err != nil {
		t.Fatal(err)
	} else {
		found := 0
		for _, status := range statuses {
			if status.Box == id {
				found++
				if status.Stack != spec.Stack.Hash {
					t.Fatalf("stack %q, want %q", status.Stack, spec.Stack.Hash)
				}
			}
		}
		if found != 1 {
			t.Fatalf("%d containers read as this box: %+v", found, statuses)
		}
	}
	podmanCommand(t, "exec", db, "sh", "-c", "echo kept > /data/volume-file")
	podmanCommand(t, "exec", string(id), "sh", "-c", "echo kept > /box/work-file")

	t.Log("sidecars follow the box's dormancy")
	if err := b.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if running := exists("ps", "--filter", "label=dyna.stack="+string(id), "--filter", "status=running", "--format", "{{.Names}}"); running != "" {
		t.Fatalf("still running: %s", running)
	}
	if running := exists("ps", "--filter", "name="+string(id), "--filter", "status=running", "--format", "{{.Names}}"); running != "" {
		t.Fatalf("still running: %s", running)
	}
	if err := b.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	boxSays("hello", "echo hello | nc -w2 db 5432")
	boxSays("kept", "cat /shared/volume-file")
	if err := b.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}

	t.Log("a swap replaces every container, and the volumes stay")
	before := exists("inspect", "--format", "{{.Id}}", db)
	spec.Stack = stack("two")
	if err := b.Recreate(ctx, spec, newImage, slice); err != nil {
		t.Fatal(err)
	}
	boxSays("new", "cat /box/woke-on")
	boxSays("kept", "cat /box/work-file")
	boxSays("kept", "cat /shared/volume-file")
	boxSays("hello", "echo hello | nc -w2 db 5432")
	if after := exists("inspect", "--format", "{{.Id}}", db); after == before {
		t.Fatal("the sidecar was not replaced")
	}
	if got := exists("exec", db, "sh", "-c", "echo $GENERATION"); got != "two" {
		t.Fatalf("sidecar generation %q", got)
	}

	t.Log("teardown leaves nothing behind")
	if err := removeHostBox(ctx, host, box.Box{ID: id, Slice: slice}, forward); err != nil {
		t.Fatal(err)
	}
	for _, listing := range [][]string{
		{"ps", "--all", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
	} {
		if out := exists(listing...); strings.Contains(out, string(id)) {
			t.Fatalf("podman %v still lists: %s", listing, out)
		}
	}
	out, err := exec.Command("systemctl", "--user", "list-units", "--all", "--no-legend", slice).CombinedOutput()
	if err != nil || strings.Contains(string(out), "active") && !strings.Contains(string(out), "inactive") {
		t.Fatalf("the slice stayed: %s %v", out, err)
	}
}

type stackProfiles struct{}

func (stackProfiles) List() []contract.ProfileSummary           { return nil }
func (stackProfiles) CompactionTokens(contract.ProfileName) int { return 0 }
func (stackProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{}, nil, nil
}

// baseTag is the tag BuildBase gives these inputs. Tagging a small image with it stands in for dyna-base, which is large.
func baseTag(pi []byte) string {
	hash := sha256.New()
	for _, input := range [][]byte{images.BaseContainerfile, pi} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(input)))
		hash.Write(size[:])
		hash.Write(input)
	}
	return fmt.Sprintf("dyna-base:%x", hash.Sum(nil))
}

// A whole session on real Podman, through the real store, API method, reconciler and box adapter: session.start picks the
// default file, the reconciler brings the box up beside its sidecar, and close leaves nothing. Only the agent is a stand-in,
// so the box never says hello, and dyna-base is a small image under its tag. The room's root runs too, as a plain box.
func TestAComposeSessionFromStartToClose(t *testing.T) {
	base, sidecarImage := os.Getenv("DYNA_PODMAN_TEST_BASE"), os.Getenv("DYNA_PODMAN_TEST_SIDECAR")
	if base == "" || sidecarImage == "" {
		t.Skip("DYNA_PODMAN_TEST_BASE and DYNA_PODMAN_TEST_SIDECAR name images; this test runs real podman")
	}
	standInPasta(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	pi := []byte(fmt.Sprintf("test-%d", time.Now().UnixNano()))
	tag := baseTag(pi)
	dir := t.TempDir()
	file := fmt.Sprintf("FROM %s\nUSER root\nRUN mkdir -p /box && chown 1000:1000 /box\n", base)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(file), 0600); err != nil {
		t.Fatal(err)
	}
	podmanCommand(t, "build", "-q", "-t", tag, dir)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", tag).Run() })

	data, err := os.MkdirTemp("", "dyna-session-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	hostData := filepath.Join(data, "host")
	script := []byte("#!/bin/sh\nexec sleep 300\n")
	agent := filepath.Join(hostData, "bin", "v1", "dyna")
	if err := os.MkdirAll(filepath.Dir(agent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, script, 0755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(data, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 1000 * box.GiB, DiskFloor: box.GiB, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	host, err := podman.New("local@host", t.TempDir(), "local", localHost{})
	if err != nil {
		t.Fatal(err)
	}
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: hostData}
	files := &compose.Files{Layout: layout}
	if err := os.MkdirAll(files.Layout.Compose(""), 0700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("services:\n  agent: {}\n  db:\n    image: %s\n    command: [sh, -c, \"nc -lk -p 5432 -e /bin/cat\"]\n    volumes: [data:/data]\nvolumes:\n  data:\n", sidecarImage)
	if err := os.WriteFile(filepath.Join(files.Layout.Compose(""), compose.Default+".yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	b := &Boxes{Store: st, Runtime: runtimeDir, Layout: layout, Version: "v1", PiTarball: pi, Context: ctx, Compose: files,
		hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, installed: map[contract.HostName]bool{"host": true}}
	sum := sha256.Sum256(script)
	b.binOnce.Do(func() { b.binSum = hex.EncodeToString(sum[:]) })
	service := engine.New(st, b, stackProfiles{}, time.Now)
	service.SetCompose(files, nil)
	sockets := api.NewSockets(api.New(&serviceWithHosts{Service: service}, nil), runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	b.Sockets = sockets
	pass := func() {
		t.Helper()
		if err := service.Reconciler().Pass(ctx); err != nil {
			t.Fatal(err)
		}
		service.Reconciler().Wait()
	}

	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, problem := service.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "stack", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	pass()
	tree, err := st.RoomTree(ctx, room.Room)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.Session(ctx, tree.Root())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), root.Box) })
	if root.Compose != "" {
		t.Fatalf("root took compose file %q", root.Compose)
	}
	if out, err := exec.Command("podman", "pod", "exists", box.PodName(root.Box)).CombinedOutput(); err == nil {
		t.Fatalf("a plain box got a pod: %s", out)
	}

	listed, problem := service.ComposeList(ctx, owner, contract.ComposeListRequest{Room: room.Room})
	if problem != nil || len(listed.Files) != 1 || listed.Files[0].Name != compose.Default || listed.Files[0].Level != contract.ComposeGlobal || listed.Files[0].Problem != "" {
		t.Fatalf("list %+v: %v", listed, problem)
	}
	started, problem := service.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	session, err := st.Session(ctx, started.Session)
	if err != nil || session.Compose != compose.Default {
		t.Fatalf("compose %q: %v", session.Compose, err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), session.Box) })
	pass()
	record, err := st.Box(ctx, session.Box)
	if err != nil || record.State != contract.BoxStarting || record.Image != tag {
		t.Fatalf("box %+v: %v", record, err)
	}
	for ctx.Err() == nil {
		out, err := exec.Command("podman", "exec", string(session.Box), "sh", "-c", "echo hello | nc -w2 db 5432").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "hello" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("the box never reached its sidecar")
	}

	if _, problem := service.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session.ID, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	for range 5 {
		pass()
	}
	for _, listing := range [][]string{
		{"ps", "--all", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
	} {
		if out := podmanCommand(t, listing...); strings.Contains(out, string(session.Box)) {
			t.Fatalf("podman %v still lists: %s", listing, out)
		}
	}
	if _, found, err := st.OpenSession(ctx, session.ID); err != nil || found {
		t.Fatalf("the session is still open: %v", err)
	}
}
