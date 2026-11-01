package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/compose"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

func stackSpec(stack *box.Stack) lifecycle.BoxSpec {
	return lifecycle.BoxSpec{Box: "box_1", Room: "room", Slice: "dyna-box_1.slice", Stack: stack}
}

func TestABoxWithNoStackIsUnchanged(t *testing.T) {
	b := &Boxes{Layout: box.Layout{Data: "/data", HostData: "/host"}}
	_, plain := b.container(stackSpec(nil), "image")
	if plain.Pod != "" || plain.Volumes != nil || len(plain.Labels) != 1 {
		t.Fatalf("%+v", plain)
	}
}

func TestABoxJoinsItsPodOnlyForSidecars(t *testing.T) {
	b := &Boxes{Layout: box.Layout{Data: "/data", HostData: "/host"}}
	env := []box.EnvVar{{Name: "DATABASE_URL", Value: "postgres://db"}}
	volumes := []box.Volume{{Name: "shared", Target: "/shared"}}
	_, alone := b.container(stackSpec(&box.Stack{Hash: "abc", Env: env, Volumes: volumes}), "image")
	if alone.Pod != "" || !reflect.DeepEqual(alone.Volumes, volumes) || alone.Env[len(alone.Env)-1] != env[0] {
		t.Fatalf("%+v", alone)
	}
	if !slices.Contains(alone.Labels, box.Label{Key: podman.StackHashLabel, Value: "abc"}) {
		t.Fatalf("labels %+v", alone.Labels)
	}
	_, joined := b.container(stackSpec(&box.Stack{Hash: "abc", Sidecars: []box.Sidecar{{Name: "db"}}}), "image")
	if joined.Pod != "dyna-pod-box_1" {
		t.Fatalf("%+v", joined)
	}
}

// A compose file comes after Dyna's own variables, so the file must not be able to name one.
func TestEveryVariableDynaSetsIsReserved(t *testing.T) {
	b := &Boxes{Layout: box.Layout{Data: "/data", HostData: "/host"}}
	_, c := b.container(stackSpec(nil), "image")
	names := []string{"HOME", "PI_CODING_AGENT_DIR"} // from CreateContainer
	for _, e := range c.Env {
		names = append(names, e.Name)
	}
	for _, name := range names {
		if !compose.Reserved(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
}

func TestTeardownRemovesTheStack(t *testing.T) {
	h := fakehost.New()
	if err := removeHostBox(context.Background(), h, box.Box{ID: "box_1", Slice: "dyna-box_1.slice"}, box.Forward{}); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, call := range h.Calls {
		order = append(order, string(call))
	}
	want := "stop-container remove-forward remove-container remove-pod remove-volume remove-stack-volumes remove-slice"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestImageTagsKeepEverySidecarImage(t *testing.T) {
	image := box.Image{Tag: "agent", Stack: &box.Stack{Sidecars: []box.Sidecar{{Image: "dyna-img-pg:1"}}}}
	if got := image.Tags(); !reflect.DeepEqual(got, []string{"agent", "dyna-img-pg:1"}) {
		t.Fatalf("got %v", got)
	}
	if (box.Image{Tag: "a"}).StackHash() != "" {
		t.Fatal("no stack has a hash")
	}
}

// stackRunner answers as a host that has every image, and fails any command that holds fail.
type stackRunner struct {
	commands []string
	fail     string
}

func (r *stackRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		return nil, nil
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	body, status := "", 0
	switch {
	case r.fail != "" && strings.Contains(command, r.fail):
		status = 125
	case strings.Contains(command, "dyna-probe"):
		body = "sum\n"
	case strings.Contains(command, "label=dyna.stack="):
		body = `[{"Names":["dyna-sidecar-box_1-db"],"Labels":{"dyna.stack.order":"0","dyna.stack.wait":"0"},"State":"running"}]`
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
}

func (r *stackRunner) index(t *testing.T, part string) int {
	t.Helper()
	i := slices.IndexFunc(r.commands, func(command string) bool { return strings.Contains(command, part) })
	if i < 0 {
		t.Fatalf("no command holds %q: %v", part, r.commands)
	}
	return i
}

type composeStore struct {
	boxStore
}

func (composeStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	size, _ := box.Size(contract.BoxSizeAgent)
	return box.Box{ID: id, Session: "ses_1", Host: "host", Size: size}, nil
}

func (composeStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{ID: "ses_1", Room: "room_1", Box: "box_1", Compose: "default"}, nil
}

func creatingBoxes(t *testing.T, r *stackRunner) (*Boxes, lifecycle.BoxSpec) {
	t.Helper()
	runtimeDir := t.TempDir()
	host, err := podman.New("user@host", runtimeDir, "host", r)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sockets := api.NewSockets(api.New(&serviceWithHosts{Service: engine.New(st, nil, nil, time.Now)}, nil), runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	files := &compose.Files{Layout: box.Layout{Data: t.TempDir()}}
	dir := files.Layout.Compose("")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "default.yaml"), []byte("services:\n  agent: {}\n  db:\n    image: postgres:16\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: composeStore{}, Compose: files, Sockets: sockets, Runtime: runtimeDir, Version: "v1", Layout: box.Layout{HostData: "/host"}, hosts: map[contract.HostName]*podman.Host{"host": host}}
	b.binOnce.Do(func() { b.binSum = "sum" })
	size, _ := box.Size(contract.BoxSizeAgent)
	return b, lifecycle.BoxSpec{Box: "box_1", Session: "ses_1", Room: "room_1", Host: "host", Slice: "dyna-box_1.slice", Size: size}
}

func TestCreateBringsUpSidecarsBeforeTheBox(t *testing.T) {
	r := &stackRunner{}
	b, spec := creatingBoxes(t, r)
	if _, err := b.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, step := range []string{"'pod' 'create'", "'create' '--name' 'dyna-sidecar-box_1-db'", "'create' '--name' 'box_1'", "'start' 'dyna-sidecar-box_1-db'", "'start' 'box_1'"} {
		at := r.index(t, step)
		if at < last {
			t.Fatalf("%s came too early: %v", step, r.commands)
		}
		last = at
	}
	if !strings.Contains(r.commands[r.index(t, "'create' '--name' 'box_1'")], "'--pod' 'dyna-pod-box_1'") {
		t.Fatal("the box did not join its pod")
	}
}

func TestCreateLeavesNothingWhenASidecarWillNotStart(t *testing.T) {
	r := &stackRunner{fail: "'start' 'dyna-sidecar-box_1-db'"}
	b, spec := creatingBoxes(t, r)
	if _, err := b.Create(context.Background(), spec); err == nil {
		t.Fatal("created a box whose sidecar failed")
	}
	if slices.ContainsFunc(r.commands, func(command string) bool { return strings.Contains(command, "'start' 'box_1'") }) {
		t.Fatalf("the box started without its sidecar: %v", r.commands)
	}
	for _, step := range []string{"'pod' 'rm'", "'volume' 'prune'", "'rm' '--ignore' '--volumes' 'box_1'", "'volume' 'rm' 'dyna-box-box_1'"} {
		r.index(t, step)
	}
}

// A file that broke after the session started fails its next start, as an image that cannot build does.
func TestCurrentImageRefusesAFileThatChanged(t *testing.T) {
	r := &stackRunner{}
	b, _ := creatingBoxes(t, r)
	path := filepath.Join(b.Compose.Layout.Compose(""), "default.yaml")
	if err := os.WriteFile(path, []byte("services:\n  agent: {}\n  db:\n    image: a\n    privileged: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CurrentImage(context.Background(), "ses_1", "host"); err == nil || !strings.Contains(err.Error(), "privileged is not allowed") {
		t.Fatalf("got %v", err)
	}
	if err := os.WriteFile(path, []byte("services:\n  agent: {}\n  db:\n    image: a\n    mem_limit: 100g\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CurrentImage(context.Background(), "ses_1", "host"); err == nil || !strings.Contains(err.Error(), "over the session's") {
		t.Fatalf("got %v", err)
	}
}

type closingStore struct {
	boxStore
	state contract.BoxState
}

func (s closingStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, Session: "ses", Host: "host", State: s.state}, nil
}

func (s closingStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{ID: "ses", Compose: "default"}, nil
}

type listingRunner struct{ commands []string }

func (r *listingRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if !slices.Contains(args, "-O") {
		r.commands = append(r.commands, args[len(args)-1])
	}
	const empty = "[]"
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", empty, len(empty))), nil
}

// A box that is closing starts only to archive, so a broken sidecar must not hold up the close.
func TestABoxStartsItsSidecarsUnlessItIsClosing(t *testing.T) {
	for state, want := range map[contract.BoxState]bool{contract.BoxStopped: true, contract.BoxClosing: false} {
		r := &listingRunner{}
		host, err := podman.New("user@host", t.TempDir(), "host", r)
		if err != nil {
			t.Fatal(err)
		}
		b := &Boxes{hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, Store: closingStore{state: state}}
		if err := b.Start(context.Background(), "box_1"); err != nil {
			t.Fatal(err)
		}
		listed := slices.ContainsFunc(r.commands, func(command string) bool { return strings.Contains(command, "label=dyna.stack=box_1") })
		if listed != want {
			t.Errorf("%s: sidecars started %v, want %v", state, listed, want)
		}
		if !strings.Contains(r.commands[len(r.commands)-1], "'start' 'box_1'") {
			t.Errorf("%s: the box did not start: %v", state, r.commands)
		}
	}
}
