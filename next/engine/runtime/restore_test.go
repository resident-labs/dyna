package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
)

// restoreStore is the store as an engine finds it after a restart.
type restoreStore struct {
	boxStore
	hosts []box.Host
	boxes []box.Box
}

func (s restoreStore) Hosts(context.Context) ([]box.Host, error)                  { return s.hosts, nil }
func (s restoreStore) Boxes(context.Context) ([]box.Box, error)                   { return s.boxes, nil }
func (restoreStore) SetEngineHost(context.Context, contract.HostName, bool) error { return nil }
func (restoreStore) OpenRooms(context.Context) ([]contract.RoomID, error)         { return nil, nil }
func (restoreStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "room_1"}, nil
}

// slowHost stands in for a host behind a relay. It keeps the host's real state across engine restarts: which sockets sit on
// the host, which forwards each master holds, and whether a binary arrived. Like ssh, it fails a command at once when the
// caller's context ends, and answers ok to a forward its master already holds without making a socket.
type slowHost struct {
	mu        sync.Mutex
	upload    time.Duration   // how long the binary takes to send; ctx kills it as it does ssh
	uploads   int             // times the binary started to go
	installed string          // the sum of a binary that arrived whole
	sum       string          // the sum an upload leaves
	sockets   map[string]bool // forwarded sockets on the host
	alive     bool            // a master is running
	master    map[string]bool // forwards the live master holds
	refused   int             // this many master starts fail first, as a host that does not answer yet
	commands  []string
}

func newSlowHost(upload time.Duration) *slowHost {
	return &slowHost{upload: upload, sum: "sum", sockets: make(map[string]bool), master: make(map[string]bool)}
}

// restart is what stopping the engine does to a host: its master dies, and sockets stay as dead files.
func (r *slowHost) restart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = false
	r.master = make(map[string]bool)
}

func (r *slowHost) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(args, "-M") {
		if r.refused > 0 {
			r.refused--
			return nil, errors.New("exit status 255: connection refused")
		}
		r.alive = true
		return nil, nil
	}
	if i := slices.Index(args, "-O"); i >= 0 {
		switch args[i+1] {
		case "check":
			if !r.alive {
				return nil, errors.New("exit status 255: no master")
			}
			return nil, nil
		case "cancel":
			spec := args[i+3]
			if !r.master[spec] {
				return nil, errors.New("exit status 255: no such forward")
			}
			delete(r.master, spec)
		case "forward":
			spec := args[i+3]
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if r.master[spec] {
				return nil, nil
			}
			path, _, _ := strings.Cut(spec, ":")
			if r.sockets[path] {
				return nil, errors.New("exit status 255: remote port forwarding failed: socket exists")
			}
			r.master[spec], r.sockets[path] = true, true
		}
		return nil, nil
	}
	command := args[len(args)-1]
	r.commands = append(r.commands, command)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if strings.Contains(command, "'/etc/machine-id'") {
		id := strings.Repeat("a", 32)
		return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", id, len(id), 0)), nil
	}
	if strings.Contains(command, "'dyna-probe'") {
		return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", r.installed, len(r.installed), 0)), nil
	}
	if strings.Contains(command, `rm -f -- "$2"`) {
		fields := strings.Fields(command)
		path := strings.Trim(fields[len(fields)-1], "'")
		delete(r.sockets, path)
	}
	if len(input) > 100 {
		r.uploads++
		upload := r.upload
		r.mu.Unlock()
		defer r.mu.Lock()
		select {
		case <-time.After(upload):
			r.mu.Lock()
			r.installed = r.sum
			r.mu.Unlock()
		case <-ctx.Done():
			return nil, errors.New("signal: killed")
		}
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func (r *slowHost) restored(boxes ...contract.BoxID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range boxes {
		held := false
		for spec := range r.master {
			if strings.Contains(spec, string(id)) && r.sockets[strings.SplitN(spec, ":", 2)[0]] {
				held = true
			}
		}
		if !held {
			return false
		}
	}
	return true
}

func (r *slowHost) hasBinary() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.installed != ""
}

func (r *slowHost) count(part string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, command := range r.commands {
		if strings.Contains(command, part) {
			n++
		}
	}
	return n
}

// startEngine is what one engine process does at start, up to the point the reconciler runs.
func startEngine(t *testing.T, ctx context.Context, r podman.Runner, st restoreStore) *Boxes {
	t.Helper()
	dir, err := os.MkdirTemp("", "dyna-rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	layout := box.Layout{Data: t.TempDir(), HostData: "/host"}
	b := &Boxes{Store: st, Runtime: dir, Layout: layout, Version: "v2", Context: ctx, retryDelay: time.Millisecond}
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	b.Sockets = api.NewSockets(api.New(&serviceWithHosts{Service: engine.New(state, nil, nil, time.Now)}, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = b.Sockets.Close() })
	b.hosts = make(map[contract.HostName]*podman.Host)
	for _, host := range st.hosts {
		h, err := podman.New("user@host", dir, string(host.Name), r)
		if err != nil {
			t.Fatal(err)
		}
		b.hosts[host.Name] = h
	}
	b.machineIDFile = t.TempDir() + "/machine-id"
	if err := os.WriteFile(b.machineIDFile, []byte(strings.Repeat("b", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	return b
}

func restoreFixture(ids ...contract.BoxID) restoreStore {
	st := restoreStore{hosts: []box.Host{{Name: "arch", State: box.HostReady}}}
	for _, id := range ids {
		st.boxes = append(st.boxes, box.Box{ID: id, Session: contract.SessionID("ses_" + id), Host: "arch", State: contract.BoxReady})
	}
	return st
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// The incident: a deploy's binary took 37 seconds to reach a relayed host, past the 15-second budget of a restore that sent
// it first, so the forwards never came and running boxes went lost.
func TestSlowBinaryDoesNotHoldBackForwards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newSlowHost(time.Hour)
	b := startEngine(t, ctx, r, restoreFixture("box_a", "box_b", "box_c"))
	defer func() { cancel(); b.WaitRecovered() }()
	eventually(t, "forwards", func() bool { return r.restored("box_a", "box_b", "box_c") })
	eventually(t, "the upload to start", func() bool { return r.count("dyna-install") == 1 })
}

// Restore returns at once, whatever the host is doing. The engine serves local boxes meanwhile.
func TestRestoreDoesNotWaitForAHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := startEngine(t, ctx, blockedHost{}, restoreFixture("box_a"))
	if !b.restoring("arch") {
		t.Fatal("a host that has not answered is not marked as restoring")
	}
	cancel()
	b.WaitRecovered()
}

// blockedHost takes every command and never answers, as a host that accepts and then stalls.
type blockedHost struct{}

func (blockedHost) Run(ctx context.Context, _ []byte, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRestoreRetriesUntilTheHostAnswers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newSlowHost(0)
	r.refused = 6
	b := startEngine(t, ctx, r, restoreFixture("box_a", "box_b"))
	defer func() { cancel(); b.WaitRecovered() }()
	eventually(t, "forwards after refusals", func() bool { return r.restored("box_a", "box_b") })
	if !b.restoring("arch") {
		t.Fatal("boxes get no time to reconnect after their forwards return")
	}
	eventually(t, "the binary after refusals", r.hasBinary)
}

// One host that does not answer leaves the others alone.
func TestHostsRecoverIndependently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	good := newSlowHost(0)
	st := restoreStore{hosts: []box.Host{{Name: "good", State: box.HostReady}, {Name: "down", State: box.HostReady}}, boxes: []box.Box{
		{ID: "box_a", Session: "ses_a", Host: "good", State: contract.BoxReady},
		{ID: "box_b", Session: "ses_b", Host: "down", State: contract.BoxReady},
	}}
	router := routedHosts{"good": good, "down": blockedHost{}}
	b := startEngine(t, ctx, router, st)
	defer func() { cancel(); b.WaitRecovered() }()
	eventually(t, "the good host", func() bool { return good.restored("box_a") })
	if !b.restoring("down") {
		t.Fatal("a host that never answered is not restoring")
	}
}

// routedHosts sends each host's commands to its fake, by the name in its control socket.
type routedHosts map[string]podman.Runner

func (r routedHosts) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for name, host := range r {
		if slices.ContainsFunc(args, func(arg string) bool { return strings.HasSuffix(arg, "/"+name) }) {
			return host.Run(ctx, input, args...)
		}
	}
	return nil, errors.New("no such host")
}

// A restart lands while the last engine was still restoring: every socket on the host is dead or half done, and the upload
// dies with it. The next engine finishes the job from the store.
func TestRestartDuringRestore(t *testing.T) {
	r := newSlowHost(time.Hour)
	st := restoreFixture("box_a", "box_b", "box_c")
	first, stop := context.WithCancel(context.Background())
	b := startEngine(t, first, r, st)
	eventually(t, "the upload to start", func() bool { return r.count("dyna-install") == 1 })
	stop()
	b.WaitRecovered()
	r.restart()

	r.mu.Lock()
	r.upload = 0
	r.mu.Unlock()
	second, cancel := context.WithCancel(context.Background())
	b = startEngine(t, second, r, st)
	defer func() { cancel(); b.WaitRecovered() }()
	eventually(t, "forwards after the restart", func() bool { return r.restored("box_a", "box_b", "box_c") })
	eventually(t, "the binary after the restart", r.hasBinary)
}

// The incident had two restarts 165 seconds apart. Neither engine finishes the upload here, and each must still leave every
// box with a working socket before it goes.
func TestTwoRestartsInARow(t *testing.T) {
	r := newSlowHost(time.Hour)
	st := restoreFixture("box_a", "box_b")
	for run := 1; run <= 3; run++ {
		ctx, stop := context.WithCancel(context.Background())
		b := startEngine(t, ctx, r, st)
		eventually(t, fmt.Sprintf("forwards in engine %d", run), func() bool { return r.restored("box_a", "box_b") })
		stop()
		b.WaitRecovered()
		r.restart()
	}
	if r.hasBinary() {
		t.Fatal("the fake upload should never have finished")
	}
}

// A lost box's inspection must not wait for a binary that it has no use for.
func TestInspectDoesNotWaitForTheBinary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newSlowHost(time.Hour)
	st := restoreFixture("box_a")
	host, err := podman.New("user@host", t.TempDir(), "arch", r)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: inspectStore{st}, Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Version: "v2", Runtime: t.TempDir(), hosts: map[contract.HostName]*podman.Host{"arch": host}}
	b.binOnce.Do(func() { b.binSum = "sum" })
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	_, _, _ = b.Inspect(ctx, "box_a")
	if ctx.Err() != nil {
		t.Fatal("inspection waited for the binary")
	}
	if !r.restored("box_a") {
		t.Fatal("inspection did not restore the forward")
	}
	if r.count("dyna-install") != 0 {
		t.Fatal("inspection sent the binary")
	}
}

type inspectStore struct{ restoreStore }

func (s inspectStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, Host: "arch", State: contract.BoxLost}, nil
}

// Build-ahead starts only when a host is back, so it cannot crowd out the forwards on a slow link.
func TestBuildAheadWaitsForForwards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newSlowHost(0)
	st := restoreFixture("box_a", "box_b")
	b := startEngine(t, ctx, r, st)
	defer func() { cancel(); b.WaitRecovered() }()
	b.BuildAhead(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	build := slices.IndexFunc(r.commands, func(c string) bool { return strings.Contains(c, "dyna-base") })
	install := slices.IndexFunc(r.commands, func(c string) bool { return strings.Contains(c, "dyna-install") })
	last := -1
	for i, command := range r.commands {
		if strings.Contains(command, `rm -f -- "$2"`) {
			last = i
		}
	}
	if build < 0 || install < 0 || last < 0 || build < last || build < install {
		t.Fatalf("build-ahead ran before recovery finished: build %d, install %d, forward %d", build, install, last)
	}
}

// Build-ahead on a host that is still down waits for it and ends with the engine.
func TestBuildAheadWaitsForAHostThatIsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := startEngine(t, ctx, blockedHost{}, restoreFixture("box_a"))
	done := make(chan struct{})
	go func() { defer close(done); b.BuildAhead(ctx) }()
	select {
	case <-done:
		cancel()
		t.Fatal("build-ahead did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
	b.WaitRecovered()
}

// Three lost boxes race to start after a deploy. In the incident each sent the whole binary, and three copies over the relay
// took over two minutes.
func TestInstallSendsTheBinaryOnce(t *testing.T) {
	r := newSlowHost(10 * time.Millisecond)
	host, err := podman.New("user@host", t.TempDir(), "arch", r)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Version: "v2"}
	b.binOnce.Do(func() { b.binSum = "sum" })
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if err := b.installBinary(context.Background(), host); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if r.uploads != 1 {
		t.Fatalf("the binary went %d times", r.uploads)
	}
}
