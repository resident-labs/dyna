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

func TestABoxClosedWhileItsNodeComesUpSendsTheNodeBack(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	r.touch("hold")
	session := r.start("worker")
	if err := r.s.reconciler.Pass(r.ctx); err != nil {
		t.Fatal(err)
	}
	for len(r.machines()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	if err := r.s.reconciler.Pass(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.remove("hold")
	r.s.reconciler.Wait()
	r.pass(3)
	if r.calls("destroy") != 1 || len(r.nodeRows()) != 0 || len(r.machines()) != 0 {
		t.Fatalf("%d destroys, %+v, %v", r.calls("destroy"), r.nodeRows(), r.machines())
	}
}

func TestAPendingBoxGetsANodeAndStarts(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	session := r.start("worker")
	r.pass(1)
	if got := r.box(session); got.State != contract.BoxPending || got.Reason != string(box.PendingPoolNode) {
		t.Fatalf("box %+v", got)
	}
	nodes := r.nodeRows()
	if len(nodes) != 1 || r.calls("create") != 1 || nodes[0].State != box.HostReady || nodes[0].Pool != "boats" || nodes[0].Box != r.box(session).ID || !slices.Equal(nodes[0].Rooms, []contract.RoomID{r.room}) {
		t.Fatalf("nodes %+v, %d creates", nodes, r.calls("create"))
	}
	if !strings.HasPrefix(nodes[0].SSH, "ssh://pip@boats-") || !strings.HasSuffix(nodes[0].SSH, ":22") || nodes[0].MaintainedAt.IsZero() {
		t.Fatalf("node %+v", nodes[0])
	}
	r.pass(2)
	got := r.box(session)
	if got.Host != nodes[0].Name || got.State != contract.BoxStarting {
		t.Fatalf("box %+v", got)
	}
	if _, ok := r.nodes.machines[got.Host].Containers[got.ID]; !ok {
		t.Fatal("the box is not on its machine")
	}
	if r.calls("create") != 1 {
		t.Fatalf("%d creates", r.calls("create"))
	}
	list, e := r.s.HostList(r.ctx, r.owner, contract.HostListRequest{})
	if e != nil || !slices.ContainsFunc(list.Hosts, func(h contract.HostSummary) bool { return h.Name == got.Host && h.Pool == "boats" }) {
		t.Fatalf("%+v %v", list, e)
	}
}

func TestMaxHoldsWithManyBoxesAtOnce(t *testing.T) {
	r := setupPool(t)
	r.addPool(2, true)
	r.touch("hold")
	sessions := []contract.SessionID{r.start("a"), r.start("b"), r.start("c"), r.start("d"), r.start("e")}
	var passes sync.WaitGroup
	for range 8 {
		passes.Go(func() {
			if err := r.s.reconciler.Pass(r.ctx); err != nil {
				t.Error(err)
			}
		})
	}
	passes.Wait()
	for len(r.machines()) < 2 {
		time.Sleep(time.Millisecond)
	}
	if err := r.s.reconciler.Pass(r.ctx); err != nil {
		t.Fatal(err)
	}
	if rows := r.nodeRows(); len(rows) != 2 || r.calls("create") != 2 {
		t.Fatalf("while creating: %+v, %d creates", rows, r.calls("create"))
	}
	r.remove("hold")
	r.s.reconciler.Wait()
	r.pass(4)
	placed := 0
	for _, session := range sessions {
		if r.box(session).Host != "" {
			placed++
		}
	}
	if placed != 2 || len(r.nodeRows()) != 2 || r.calls("create") != 2 {
		t.Fatalf("%d placed, %+v, %d creates", placed, r.nodeRows(), r.calls("create"))
	}
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

func TestEachBoxHasItsOwnNodeUntilItIsArchived(t *testing.T) {
	r := setupPool(t)
	if err := r.s.store.AddRoomRepo(r.ctx, r.room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	r.s.SetRepositories(&fakeRepos{})
	r.addPool(6, true)
	started, e := r.s.SessionStart(r.ctx, r.owner, contract.SessionStartRequest{Room: r.room, Parent: r.root, Name: "first", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	first, second := started.Session, r.start("second")
	r.pass(3)
	a, b := r.box(first), r.box(second)
	if a.Host == "" || b.Host == "" || a.Host == b.Host || len(r.nodeRows()) != 2 {
		t.Fatalf("%+v %+v %+v", a, b, r.nodeRows())
	}

	worker := contract.Principal{Kind: contract.PrincipalSession, Session: first}
	channel, e := testChannel(t, r.s, r.ctx, worker)
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: first}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	if got := r.box(first); got.State != contract.BoxClosing || r.calls("destroy") != 0 {
		t.Fatalf("before the archive: %+v, %d destroys", got, r.calls("destroy"))
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("no archive sent")
	}
	if _, e := r.s.ArchiveDone(r.ctx, worker, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	rows := r.nodeRows()
	if got := r.closed(a.ID); got.State != contract.BoxDeleted || r.calls("destroy") != 1 || len(rows) != 1 || rows[0].Name != b.Host {
		t.Fatalf("after the archive: %+v, %d destroys, %+v", got, r.calls("destroy"), rows)
	}
	if slices.Contains(r.machines(), string(a.Host)) || !slices.Contains(r.nodes.left, a.Host) {
		t.Fatalf("machines %v, left %v", r.machines(), r.nodes.left)
	}
}

func TestADormantSessionKeepsItsNode(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	session := r.start("worker")
	r.pass(3)
	s, ctx := r.s, r.ctx
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	if _, e := s.SessionStop(ctx, r.owner, contract.SessionStopRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	r.advance(24 * time.Hour)
	r.pass(2)
	if r.calls("destroy") != 0 || len(r.nodeRows()) != 1 {
		t.Fatalf("%d destroys, %+v", r.calls("destroy"), r.nodeRows())
	}
}

func TestSharedNodesAreReusedAndGoAfterIdle(t *testing.T) {
	r := setupPool(t)
	r.addPool(3, false)
	first := r.start("first")
	r.pass(3)
	node := r.box(first).Host
	if node == "" || r.calls("create") != 1 {
		t.Fatalf("%+v %d", r.box(first), r.calls("create"))
	}
	if rows := r.nodeRows(); rows[0].Box != "" || !slices.Equal(rows[0].Rooms, []contract.RoomID{r.room}) {
		t.Fatalf("%+v", rows)
	}
	second := r.start("second")
	r.pass(3)
	if got := r.box(second); got.Host != node || r.calls("create") != 1 {
		t.Fatalf("%+v %d", got, r.calls("create"))
	}
	for _, session := range []contract.SessionID{first, second} {
		if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
			t.Fatal(e)
		}
	}
	r.pass(3)
	r.advance(29 * time.Minute)
	r.pass(1)
	if r.calls("destroy") != 0 {
		t.Fatal("destroyed before its idle ran out")
	}
	third := r.start("third")
	r.pass(3)
	if got := r.box(third); got.Host != node || r.calls("create") != 1 {
		t.Fatalf("an empty node should take the box: %+v", got)
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: third}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	r.advance(29 * time.Minute)
	r.pass(1)
	if r.calls("destroy") != 0 {
		t.Fatal("a box should restart the wait")
	}
	r.advance(2 * time.Minute)
	r.pass(1)
	if r.calls("destroy") != 1 || len(r.nodeRows()) != 0 || len(r.machines()) != 0 {
		t.Fatalf("%d destroys, %+v", r.calls("destroy"), r.nodeRows())
	}
}

func TestAFailedCreateLeavesTheBoxWaitingAndBacksOff(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	r.touch("fail")
	session := r.start("worker")
	r.pass(2)
	got := r.box(session)
	if got.State != contract.BoxPending || got.Reason != string(box.PendingPoolFailed) || r.calls("create") != 1 || r.calls("destroy") != 1 || len(r.nodeRows()) != 0 {
		t.Fatalf("%+v, %d creates, %d destroys, %+v", got, r.calls("create"), r.calls("destroy"), r.nodeRows())
	}
	notices := r.notices()
	if len(notices) != 1 || !strings.Contains(notices[0].Text, "no capacity left") || !strings.Contains(notices[0].Text, "boats") {
		t.Fatalf("%+v", notices)
	}
	list, e := r.s.PoolList(r.ctx, r.owner, contract.PoolListRequest{})
	if e != nil || !strings.Contains(list.Pools[0].Problem, "no capacity left") {
		t.Fatalf("%+v %v", list, e)
	}
	r.pass(5)
	r.advance(50 * time.Second)
	r.pass(5)
	if r.calls("create") != 1 {
		t.Fatalf("%d creates inside the wait", r.calls("create"))
	}
	r.advance(11 * time.Second)
	r.pass(1)
	r.advance(time.Minute)
	r.pass(1)
	if r.calls("create") != 2 {
		t.Fatalf("%d creates: the second wait is two minutes", r.calls("create"))
	}
	if len(r.notices()) != 1 {
		t.Fatalf("%+v", r.notices())
	}
	r.remove("fail")
	r.advance(2 * time.Minute)
	r.pass(3)
	list, e = r.s.PoolList(r.ctx, r.owner, contract.PoolListRequest{})
	if got := r.box(session); got.Host == "" || e != nil || list.Pools[0].Problem != "" {
		t.Fatalf("%+v %+v %v", got, list, e)
	}
}

func TestANodeThatFailsTheHostCheckIsDestroyed(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	r.nodes.joinErr = errors.New("host check FUSE for the shared folder: no fuse")
	session := r.start("worker")
	r.pass(2)
	if got := r.box(session); got.State != contract.BoxPending || got.Reason != string(box.PendingPoolFailed) {
		t.Fatalf("%+v", got)
	}
	if r.calls("create") != 1 || r.calls("destroy") != 1 || len(r.machines()) != 0 || len(r.nodeRows()) != 0 {
		t.Fatalf("%d creates, %d destroys, %v, %+v", r.calls("create"), r.calls("destroy"), r.machines(), r.nodeRows())
	}
	if notices := r.notices(); len(notices) != 1 || !strings.Contains(notices[0].Text, "no fuse") {
		t.Fatalf("%+v", notices)
	}
}

func TestANodeTooSmallForItsBoxIsDestroyed(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	r.nodes.memory = 1 << 20
	session := r.start("worker")
	r.pass(2)
	if got := r.box(session); got.Reason != string(box.PendingPoolFailed) || r.calls("destroy") != 1 || len(r.nodeRows()) != 0 {
		t.Fatalf("%+v, %d destroys, %+v", got, r.calls("destroy"), r.nodeRows())
	}
	if notices := r.notices(); len(notices) != 1 || !strings.Contains(notices[0].Text, "too small") {
		t.Fatalf("%+v", notices)
	}
}

func TestAFailedDestroyKeepsTheNodeCountedAndRetries(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	session := r.start("worker")
	r.pass(3)
	r.touch("fail-destroy")
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	rows := r.nodeRows()
	if len(rows) != 1 || rows[0].State != box.HostDraining || r.calls("destroy") != 1 || len(r.notices()) != 1 {
		t.Fatalf("%+v, %d destroys, %+v", rows, r.calls("destroy"), r.notices())
	}
	next := r.start("next")
	r.pass(2)
	if r.calls("create") != 1 || r.box(next).Host != "" {
		t.Fatalf("a draining node still counts toward max: %d creates", r.calls("create"))
	}
	r.remove("fail-destroy")
	r.advance(time.Minute)
	r.pass(1)
	list, e := r.s.PoolList(r.ctx, r.owner, contract.PoolListRequest{})
	if len(r.nodeRows()) != 0 || e != nil || list.Pools[0].Problem != "" {
		t.Fatalf("%+v %+v %v", r.nodeRows(), list, e)
	}
	r.pass(3)
	if r.box(next).Host == "" {
		t.Fatalf("%+v", r.box(next))
	}
}

func TestAnUnknownMachineIsReportedOnceAndLeftAlone(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	r.touch("machines/stray")
	r.pass(1)
	notices := r.notices()
	if len(notices) != 1 || !strings.Contains(notices[0].Text, "stray") {
		t.Fatalf("%+v", notices)
	}
	for range 3 {
		r.advance(11 * time.Minute)
		r.pass(1)
	}
	if len(r.notices()) != 1 || r.calls("destroy") != 0 || !slices.Contains(r.machines(), "stray") {
		t.Fatalf("%+v, %d destroys, %v", r.notices(), r.calls("destroy"), r.machines())
	}
}

func TestANodeTheScriptLostIsALostHostAndADiscardEndsIt(t *testing.T) {
	r := setupPool(t)
	r.addPool(6, true)
	r.s.reconciler.SetRoomHosts(&fakeRoomHosts{})
	session := r.start("worker")
	r.pass(3)
	worker := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, problem := r.s.BoxHello(r.ctx, worker, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	channel, problem := r.s.BoxChannel(r.ctx, worker, "")
	if problem != nil {
		t.Fatal(problem)
	}
	go func() {
		for range channel {
		}
	}()
	placed := r.box(session)
	r.remove("machines/" + string(placed.Host))
	r.advance(11 * time.Minute)
	r.pass(2)
	rows := r.nodeRows()
	if len(rows) != 1 || rows[0].State != box.HostUnreachable || r.box(session).State != contract.BoxLost || len(r.notices()) != 0 {
		t.Fatalf("%+v %+v %+v", rows, r.box(session), r.notices())
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session, Discard: true}); e != nil {
		t.Fatal(e)
	}
	r.pass(4)
	if got := r.closed(placed.ID); got.State != contract.BoxDeleted || len(r.nodeRows()) != 0 || r.calls("destroy") != 1 {
		t.Fatalf("%+v, %+v, %d destroys", got, r.nodeRows(), r.calls("destroy"))
	}
}

func TestOnlyRestrictsWhereNewBoxesGo(t *testing.T) {
	r := setupPool(t)
	if err := r.s.store.AddHost(r.ctx, box.Host{Name: "other", State: box.HostReady, CPUs: 4, MemBytes: 8 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: r.s.now()}); err != nil {
		t.Fatal(err)
	}
	placed := r.box(r.root)
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: []string{"other"}}); e != nil {
		t.Fatal(e)
	}
	session := r.start("worker")
	r.pass(2)
	if got := r.box(session); got.Host != "other" {
		t.Fatalf("%+v", got)
	}
	if got := r.box(r.root); got.Host != placed.Host {
		t.Fatalf("a placed box moved: %+v", got)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room}); e != nil {
		t.Fatal(e)
	}
	if only, err := r.s.store.RoomOnly(r.ctx, r.room); err != nil || len(only) != 0 {
		t.Fatalf("%v %v", only, err)
	}
	for _, bad := range [][]string{{"nothing"}, {"host", "host"}} {
		if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: bad}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("%v: %v", bad, e)
		}
	}
	r.addPool(1, false)
	second := contract.PoolAddRequest{Name: "ships", Script: "#!/bin/sh\n", Max: 1}
	if _, e := r.s.PoolAdd(r.ctx, r.owner, second); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: []string{"boats", "ships"}}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("two pools: %v", e)
	}
}

func TestARoomThatNamesNoPoolStaysOffItsNodes(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, false)
	session := r.start("worker")
	r.pass(3)
	node := r.box(session).Host
	other, e := r.s.RoomCreate(r.ctx, r.owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	r.pass(1)
	outside, e := r.s.SessionStart(r.ctx, r.owner, contract.SessionStartRequest{Room: other.Room, Name: "outside", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	r.pass(2)
	if got := r.box(outside.Session); got.Host != "host" {
		t.Fatalf("%+v", got)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: other.Room, Only: []string{"boats"}}); e != nil {
		t.Fatal(e)
	}
	rows := r.nodeRows()
	if !slices.Contains(rows[0].Rooms, other.Room) {
		t.Fatalf("a shared node should serve a room that names its pool: %+v", rows)
	}
	joined, e := r.s.SessionStart(r.ctx, r.owner, contract.SessionStartRequest{Room: other.Room, Name: "joined", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	r.pass(2)
	if got := r.box(joined.Session); got.Host != node {
		t.Fatalf("%+v", got)
	}
}

func TestDeletingARoomIsNotHeldByItsNode(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	r.start("worker")
	r.pass(3)
	if _, e := r.s.RoomDelete(r.ctx, r.owner, contract.RoomDeleteRequest{Room: r.room, Discard: true}); e != nil {
		t.Fatal(e)
	}
	r.pass(5)
	if len(r.nodeRows()) != 0 || r.calls("destroy") != 1 {
		t.Fatalf("%+v, %d destroys", r.nodeRows(), r.calls("destroy"))
	}
}

func TestASessionAsksTheOwnerForPoolsAndRules(t *testing.T) {
	r := setupPool(t)
	worker := contract.Principal{Kind: contract.PrincipalSession, Session: r.start("worker")}
	req := contract.PoolAddRequest{Name: "boats", Script: strings.ReplaceAll(poolScript, "DIR", r.dir), Max: 2, PerSession: true}
	added, e := r.s.PoolAdd(r.ctx, worker, req)
	if e != nil || added.Operation == "" {
		t.Fatalf("%+v %v", added, e)
	}
	list, e := r.s.PoolList(r.ctx, worker, contract.PoolListRequest{})
	if e != nil || len(list.Pools) != 0 {
		t.Fatalf("a session added a pool itself: %+v %v", list, e)
	}
	if _, err := os.Stat(r.s.layout.PoolScript("boats")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	asked := eventsOf[contract.OpRequested](t, r.s, r.ctx, r.room)
	if len(asked) != 1 || !strings.Contains(asked[0].Card.Body, "no capacity left") {
		t.Fatalf("the card should show the script: %+v", asked)
	}
	decide := func(id contract.OpID) {
		t.Helper()
		if _, e := r.s.OpDecide(r.ctx, r.owner, contract.OpDecideRequest{ID: id, Approve: true}); e != nil {
			t.Fatal(e)
		}
		for range 2000 {
			record, err := r.s.store.Operation(r.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if record.Operation.State == contract.OpDone {
				return
			}
			if record.Operation.State == contract.OpFailed {
				t.Fatalf("%+v", record.Operation.Result)
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("the operation did not end")
	}
	decide(added.Operation)
	if list, e = r.s.PoolList(r.ctx, worker, contract.PoolListRequest{}); e != nil || len(list.Pools) != 1 || list.Pools[0].Idle != 0 {
		t.Fatalf("%+v %v", list, e)
	}
	if info, err := os.Stat(r.s.layout.PoolScript("boats")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, e := r.s.PoolRemove(r.ctx, worker, contract.PoolRemoveRequest{Name: "boats"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("%v", e)
	}
	other, e := r.s.RoomCreate(r.ctx, r.owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.RoomHosts(r.ctx, worker, contract.RoomHostsRequest{Room: other.Room, Only: []string{"boats"}}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("another room: %v", e)
	}
	rule, e := r.s.RoomHosts(r.ctx, worker, contract.RoomHostsRequest{Room: r.room, Only: []string{"boats"}})
	if e != nil || rule.Operation == "" {
		t.Fatalf("%+v %v", rule, e)
	}
	if only, err := r.s.store.RoomOnly(r.ctx, r.room); err != nil || len(only) != 0 {
		t.Fatalf("the rule took effect unapproved: %v %v", only, err)
	}
	decide(rule.Operation)
	if only, err := r.s.store.RoomOnly(r.ctx, r.room); err != nil || !slices.Equal(only, []string{"boats"}) {
		t.Fatalf("%v %v", only, err)
	}
}

func TestPoolAddChecksItsShape(t *testing.T) {
	r := setupPool(t)
	good := contract.PoolAddRequest{Name: "boats", Script: "#!/bin/sh\n", Max: 1}
	for name, change := range map[string]func(*contract.PoolAddRequest){
		"no max":     func(q *contract.PoolAddRequest) { q.Max = 0 },
		"bad name":   func(q *contract.PoolAddRequest) { q.Name = "../boats" },
		"host name":  func(q *contract.PoolAddRequest) { q.Name = "host" },
		"no shebang": func(q *contract.PoolAddRequest) { q.Script = "echo hi" },
		"too large":  func(q *contract.PoolAddRequest) { q.Script = "#!" + strings.Repeat("x", 64<<10) },
	} {
		req := good
		change(&req)
		if _, e := r.s.PoolAdd(r.ctx, r.owner, req); e == nil || e.Code != contract.ErrInvalid {
			t.Errorf("%s: %v", name, e)
		}
	}
	if _, e := r.s.PoolAdd(r.ctx, r.owner, good); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.PoolAdd(r.ctx, r.owner, good); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("%v", e)
	}
}

func TestAPoolInUseCannotBeRemoved(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, true)
	session := r.start("worker")
	r.pass(3)
	_, e := r.s.PoolRemove(r.ctx, r.owner, contract.PoolRemoveRequest{Name: "boats"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "boats-") || !strings.Contains(e.Message, string(r.room)) {
		t.Fatalf("%v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.PoolRemove(r.ctx, r.owner, contract.PoolRemoveRequest{Name: "boats"}); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Stat(r.s.layout.Pool("boats")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestHostRemoveRefusesWhileABoxIsOnIt(t *testing.T) {
	r := setupPool(t)
	if err := r.s.store.AddHost(r.ctx, box.Host{Name: "spare", State: box.HostReady, CPUs: 4, MemBytes: 8 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: r.s.now()}); err != nil {
		t.Fatal(err)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: []string{"spare"}}); e != nil {
		t.Fatal(e)
	}
	session := r.start("worker")
	r.pass(2)
	placed := r.box(session)
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "session worker") {
		t.Fatalf("%v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "--only") {
		t.Fatalf("%v", e)
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.HostRemove(r.ctx, contract.Principal{Kind: contract.PrincipalSession, Session: r.root}, contract.HostRemoveRequest{Host: "spare"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("%v", e)
	}
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare"}); e != nil {
		t.Fatal(e)
	}
	hosts, err := r.s.store.Hosts(r.ctx)
	if err != nil || slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == "spare" }) || !slices.Contains(r.nodes.left, "spare") {
		t.Fatalf("%+v %v %v", hosts, r.nodes.left, err)
	}
	if got := r.closed(placed.ID); got.Host != "" {
		t.Fatalf("a deleted box still names the host: %+v", got)
	}
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare"}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("%v", e)
	}
}

func TestHostRemoveSendsANodeBackThroughItsScript(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, false)
	session := r.start("worker")
	r.pass(3)
	node := r.box(session).Host
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: node}); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("%v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	r.pass(3)
	if _, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: node}); e != nil {
		t.Fatal(e)
	}
	r.pass(1)
	if r.calls("destroy") != 1 || len(r.nodeRows()) != 0 {
		t.Fatalf("%d destroys, %+v", r.calls("destroy"), r.nodeRows())
	}
}

func TestARunNeverTakesANode(t *testing.T) {
	r := setupPool(t)
	r.addPool(1, false)
	r.start("worker")
	r.pass(3)
	sub := store.Subscription{}
	sub.Room = r.room
	sub.Run = &contract.RunSpec{}
	host, err := r.s.placeRun(r.ctx, sub, "run", contract.BoxSize{Name: contract.BoxSizeAgent, CPUs: 1, MemBytes: box.GiB})
	if err != nil || host != "" {
		t.Fatalf("%q %v", host, err)
	}
}
