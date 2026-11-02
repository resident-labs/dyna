package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

// poolScript is a real pool: a folder of files stands for its machines. Marker files make it fail, or hold a create.
const poolScript = `#!/bin/sh
dir=DIR
case "$1" in
create)
	echo "create $2 $3" >> "$dir/log"
	if [ -e "$dir/fail" ]; then echo "no capacity left" >&2; exit 3; fi
	touch "$dir/machines/$2"
	while [ -e "$dir/hold" ]; do sleep 0.01; done
	printf '{"ssh": "pip@%s:22"}' "$2"
	;;
destroy)
	echo "destroy $2" >> "$dir/log"
	if [ -e "$dir/fail-destroy" ]; then echo "cannot destroy" >&2; exit 4; fi
	rm -f "$dir/machines/$2"
	;;
list)
	printf '['
	sep=
	for file in "$dir"/machines/*; do
		[ -e "$file" ] || continue
		printf '%s"%s"' "$sep" "$(basename "$file")"
		sep=,
	done
	printf ']'
	;;
esac
`

// scriptNodes runs the real script. Join makes each machine a fakehost, in place of the host check.
type scriptNodes struct {
	lifecycle.Script
	mu       sync.Mutex
	machines map[contract.HostName]*fakehost.Host
	left     []contract.HostName
	joinErr  error
	memory   int64
}

func (n *scriptNodes) Join(_ context.Context, name contract.HostName, ssh string) (box.Host, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.joinErr != nil {
		return box.Host{}, n.joinErr
	}
	n.machines[name] = fakehost.New()
	return box.Host{Name: name, SSH: ssh, State: box.HostReady, CPUs: 8, MemBytes: n.memory, DiskBytes: 200 * box.GiB, CheckedAt: time.Unix(1000, 0)}, nil
}

func (n *scriptNodes) Leave(_ context.Context, name contract.HostName) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.left = append(n.left, name)
	delete(n.machines, name)
}

// nodeBoxes puts a box's container on its machine.
type nodeBoxes struct {
	fakeBoxes
	nodes *scriptNodes
}

func (b *nodeBoxes) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.nodes.mu.Lock()
	defer b.nodes.mu.Unlock()
	if h := b.nodes.machines[spec.Host]; h != nil {
		return "image", h.CreateContainer(ctx, box.ContainerSpec{Box: spec.Box})
	}
	return "image", nil
}

func (b *nodeBoxes) Remove(ctx context.Context, id contract.BoxID) error {
	b.nodes.mu.Lock()
	defer b.nodes.mu.Unlock()
	for _, h := range b.nodes.machines {
		if _, ok := h.Containers[id]; ok {
			return h.RemoveContainer(ctx, id)
		}
	}
	return nil
}

type poolRig struct {
	t     *testing.T
	s     *Service
	ctx   context.Context
	room  contract.RoomID
	root  contract.SessionID
	dir   string
	nodes *scriptNodes
	clock *atomic.Int64
	owner contract.Principal
}

func setupPool(t *testing.T) *poolRig {
	t.Helper()
	data, dir := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "machines"), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	clock := &atomic.Int64{}
	clock.Store(1000)
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now()}); err != nil {
		t.Fatal(err)
	}
	layout := box.Layout{Data: data, HostData: "/host"}
	nodes := &scriptNodes{Script: lifecycle.Script{Layout: layout}, machines: make(map[contract.HostName]*fakehost.Host), memory: 32 * box.GiB}
	s := New(st, &nodeBoxes{nodes: nodes}, fakeProfiles{}, now)
	s.SetLayout(layout)
	s.reconciler.SetNodes(nodes)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pool.add": s.PoolAddEntry(), "room.hosts": s.RoomHostsEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	boxes, err := st.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &poolRig{t: t, s: s, ctx: ctx, room: room.Room, root: boxes[0].Session, dir: dir, nodes: nodes, clock: clock, owner: owner}
}

// addPool adds the script as a pool and keeps the room on it.
func (r *poolRig) addPool(max int, perSession bool) {
	r.t.Helper()
	req := contract.PoolAddRequest{Name: "boats", Script: strings.ReplaceAll(poolScript, "DIR", r.dir), Max: max, PerSession: perSession}
	if _, e := r.s.PoolAdd(r.ctx, r.owner, req); e != nil {
		r.t.Fatal(e)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: []string{"boats"}}); e != nil {
		r.t.Fatal(e)
	}
}

func (r *poolRig) start(name string) contract.SessionID {
	r.t.Helper()
	session, e := r.s.SessionStart(r.ctx, r.owner, contract.SessionStartRequest{Room: r.room, Name: name, Profile: "test"})
	if e != nil {
		r.t.Fatal(e)
	}
	return session.Session
}

func (r *poolRig) pass(times int) {
	r.t.Helper()
	for range times {
		passLifecycle(r.t, r.s, r.ctx)
	}
}

func (r *poolRig) advance(by time.Duration) { r.clock.Add(int64(by / time.Second)) }

func (r *poolRig) touch(name string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), nil, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *poolRig) remove(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
		r.t.Fatal(err)
	}
}

// calls counts the lines of the script's log that start with a word.
func (r *poolRig) calls(word string) int {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		r.t.Fatal(err)
	}
	count := 0
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, word+" ") {
			count++
		}
	}
	return count
}

func (r *poolRig) machines() []string {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.dir, "machines"))
	if err != nil {
		r.t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func (r *poolRig) nodeRows() []box.Host {
	r.t.Helper()
	nodes, err := r.s.store.PoolNodes(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return nodes
}

func (r *poolRig) box(id contract.SessionID) box.Box {
	r.t.Helper()
	session, err := r.s.store.Session(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	b, err := r.s.store.Box(r.ctx, session.Box)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

// closed reads a box whose session has closed.
func (r *poolRig) closed(id contract.BoxID) box.Box {
	r.t.Helper()
	b, err := r.s.store.Box(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *poolRig) notices() []contract.NoticePosted {
	r.t.Helper()
	return eventsOf[contract.NoticePosted](r.t, r.s, r.ctx, r.room)
}

func TestARestartMidCreateUndoesItAndKeepsMax(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	r.touch("hold")
	session := r.start("worker")
	engine, stop := context.WithCancel(r.ctx)
	if err := r.s.reconciler.Pass(engine); err != nil {
		t.Fatal(err)
	}
	if err := r.s.reconciler.Pass(engine); err != nil {
		t.Fatal(err)
	}
	for len(r.machines()) == 0 {
		time.Sleep(time.Millisecond)
	}
	stop()
	r.s.reconciler.Wait()
	rows := r.nodeRows()
	if len(rows) != 1 || rows[0].State != box.HostProvisioning {
		t.Fatalf("after the engine stopped: %+v", rows)
	}
	first := rows[0].Name
	hosts, err := r.s.store.Hosts(r.ctx)
	if err != nil || slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == first }) {
		t.Fatalf("a node being created is a host: %+v %v", hosts, err)
	}
	r.remove("hold")

	r.s.reconciler = lifecycle.New(r.s.store, r.s.boxes, r.s, nil, r.s.event, r.s.now)
	r.s.reconciler.SetNodes(r.nodes)
	r.pass(1)
	if r.calls("destroy") != 1 || r.calls("create") != 1 || len(r.machines()) != 0 || len(r.nodeRows()) != 0 {
		t.Fatalf("after the restart: %d destroys, %d creates, %v, %+v", r.calls("destroy"), r.calls("create"), r.machines(), r.nodeRows())
	}
	r.pass(1)
	rows = r.nodeRows()
	if len(rows) != 1 || rows[0].Name == first || r.calls("create") != 2 || len(r.machines()) != 1 {
		t.Fatalf("the new node: %+v, %d creates", rows, r.calls("create"))
	}
	r.pass(2)
	if got := r.box(session); got.Host != rows[0].Name {
		t.Fatalf("box %+v", got)
	}
}
