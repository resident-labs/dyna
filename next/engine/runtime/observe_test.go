package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
)

func TestInspectContainer(t *testing.T) {
	h := fakehost.New()
	h.Containers["run"] = box.ContainerSpec{Box: "run"}
	h.Running["run"] = true
	h.Containers["stop"] = box.ContainerSpec{Box: "stop"}
	for _, tc := range []struct {
		id               contract.BoxID
		present, running bool
	}{{"run", true, true}, {"stop", true, false}, {"missing", false, false}} {
		status, present, err := inspectContainer(context.Background(), h, tc.id)
		if err != nil || present != tc.present || (present && (status.Box != tc.id || status.Running != tc.running)) {
			t.Fatalf("inspect %s: %+v present=%v err=%v", tc.id, status, present, err)
		}
	}
	if len(h.Calls) != 3 {
		t.Fatalf("host calls=%v, want one list per inspection", h.Calls)
	}
	h.FailNext = errors.New("offline")
	if _, _, err := inspectContainer(context.Background(), h, "run"); err == nil {
		t.Fatal("host error was ignored")
	}
}

type heartbeatRecord struct {
	ok    bool
	free  *int64
	total int64
}
type recordingHeartbeats struct {
	records []heartbeatRecord
	gpus    [][]contract.GPU
}

func (r *recordingHeartbeats) SetHostGPUs(_ context.Context, _ contract.HostName, gpus []contract.GPU) error {
	r.gpus = append(r.gpus, gpus)
	return nil
}

func (r *recordingHeartbeats) RecordHostHeartbeat(_ context.Context, _ contract.HostName, ok bool, free *int64, total int64, _ time.Time) error {
	r.records = append(r.records, heartbeatRecord{ok: ok, free: free, total: total})
	return nil
}

func TestReportHeartbeatFreeDisk(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	records := &recordingHeartbeats{}
	b := &Boxes{HeartbeatStore: records, Pressure: engine.New(st, nil, nil, time.Now)}
	b.reportHeartbeat(context.Background(), "host", true, podman.Load{DiskBytes: 100, DiskUsed: 30}, time.Time{})
	b.reportHeartbeat(context.Background(), "host", true, podman.Load{}, time.Time{})
	b.reportHeartbeat(context.Background(), "host", false, podman.Load{DiskBytes: 100}, time.Time{})
	if len(records.records) != 3 || records.records[0].free == nil || *records.records[0].free != 70 || records.records[0].total != 100 || records.records[1].free != nil || records.records[2].free != nil || records.records[2].ok {
		t.Fatalf("heartbeat records=%+v", records.records)
	}
}

// The same sample drives the notice, stored host.list flag and live flag consumed by the web.
func TestDiskNoticeAndHostFlagsAgree(t *testing.T) {
	for _, tc := range []struct {
		name        string
		total, free int64
		low         bool
	}{
		// Notices truncate GiB: these floors render as 187 and 92, while the old warn levels exceed 281 and 172.
		{"control-plane report", 1879 * box.GiB, 281 * box.GiB, false},
		{"desktop report", 929 * box.GiB, 172 * box.GiB, false},
		{"above", 1000 * box.GiB, 100*box.GiB + 1, false},
		{"equal", 1000 * box.GiB, 100 * box.GiB, false},
		{"below", 1000 * box.GiB, 100*box.GiB - 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0)`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.AddHost(ctx, box.Host{Name: "h", State: box.HostReady}); err != nil {
				t.Fatal(err)
			}
			service := engine.New(st, nil, nil, time.Now)
			b := &Boxes{Store: st}
			at := time.Unix(1000, 0)
			for _, name := range []contract.HostName{"h", contract.ControlPlane} {
				if err := st.RecordHostHeartbeat(ctx, name, true, &tc.free, tc.total, at); err != nil {
					t.Fatal(err)
				}
				b.record(name, true, podman.Load{DiskBytes: tc.total, DiskUsed: tc.total - tc.free}, at)
			}
			list, problem := service.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			if len(list.Hosts) != 2 || list.Hosts[1].DiskLow != tc.low {
				t.Fatalf("stored host.list: %+v, want low %v", list.Hosts, tc.low)
			}
			if err := b.HostLoad(ctx, list.Hosts); err != nil {
				t.Fatal(err)
			}
			for _, host := range list.Hosts {
				if host.DiskLow != tc.low {
					t.Fatalf("live %s: low %v, want %v", host.Name, host.DiskLow, tc.low)
				}
			}
			events, err := st.Events(ctx, "r", 0, 100, []contract.EventType{"host.disk_low"})
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.low {
				want = 2
			} // The control-plane data disk and registered host disk have separate samples and crossing keys.
			if len(events) != want {
				t.Fatalf("notices: %d, want %d", len(events), want)
			}
			for _, event := range events {
				low := event.Body.(contract.HostDiskLow)
				if low.FreeBytes != tc.free || low.FloorBytes != box.DiskFloor(tc.total) {
					t.Fatalf("notice: %+v", low)
				}
			}
		})
	}
}

type offlineRestoreStore struct{ boxStore }

func (offlineRestoreStore) Hosts(context.Context) ([]box.Host, error) {
	return engineHosts("h"), nil
}
func (offlineRestoreStore) SetEngineHost(context.Context, contract.HostName, bool) error { return nil }
func (offlineRestoreStore) Boxes(context.Context) ([]box.Box, error) {
	return []box.Box{{ID: "b", Session: "s", Host: "h", State: contract.BoxStopped}}, nil
}
func (offlineRestoreStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, Session: "s", Host: "h", State: contract.BoxStopped}, nil
}
func (offlineRestoreStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "r"}, nil
}

type offlineRunner struct {
	mu               sync.Mutex
	offline          bool
	forward, started bool
}

func (r *offlineRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline {
		return nil, errors.New("host offline")
	}
	for i, arg := range args {
		if arg == "-O" && i+1 < len(args) {
			if args[i+1] == "forward" {
				r.forward = true
			}
			return nil, nil
		}
	}
	command := args[len(args)-1]
	if strings.Contains(command, "'podman' '--cgroup-manager=systemd' 'start'") {
		if !r.forward {
			return nil, errors.New("start before forward")
		}
		r.started = true
	}
	body := ""
	if strings.Contains(command, "dyna-probe") {
		body = strings.Repeat("a", 64) + "\n"
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}

func TestRestoreToleratesOfflineHost(t *testing.T) {
	runtimeDir := t.TempDir()
	runner := &offlineRunner{offline: true}
	host, err := podman.New("user@host", runtimeDir, "h", runner)
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
	b := &Boxes{Store: offlineRestoreStore{}, Sockets: sockets, Runtime: runtimeDir, Layout: box.Layout{HostData: filepath.Join(runtimeDir, "host")}, Version: "v1", hosts: map[contract.HostName]*podman.Host{"h": host}}
	b.binOnce.Do(func() { b.binSum = strings.Repeat("a", 64) })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); b.WaitRecovered() }()
	if err := b.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "boxes", "b.sock")); err != nil {
		t.Fatalf("local box socket: %v", err)
	}
	runner.mu.Lock()
	runner.offline = false
	runner.mu.Unlock()
	if err := b.Start(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if !runner.forward || !runner.started {
		t.Fatalf("forward=%v started=%v", runner.forward, runner.started)
	}
}

// An exec-only host's sample reaches the summary as its CPU, memory and disk figures, and its disk is never low, even at 99% used.
func TestHostLoadShowsExecOnlyFiguresAndNeverDiskLow(t *testing.T) {
	b := &Boxes{Store: listStore{}}
	total := 1000 * box.GiB
	now := time.Now()
	b.record("omarchy", true, podman.Load{ExecOnly: true, CPUs: 8, CPUUsed: 2.5, MemBytes: 16 * box.GiB, MemUsed: 4 * box.GiB, DiskBytes: total, DiskUsed: total * 99 / 100}, now)
	hosts := []contract.HostSummary{{Name: "omarchy", ExecOnly: true}, {Name: "full"}}
	if err := b.HostLoad(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	got := hosts[0]
	if !got.Reachable || got.CPUs != 8 || got.CPUUsed != 2.5 || got.MemBytes != 16*box.GiB || got.MemUsed != 4*box.GiB || got.DiskBytes != total || got.DiskUsed != total*99/100 || got.DiskLow || len(got.History) != 1 {
		t.Fatalf("summary %+v", got)
	}
	if hosts[1].CPUs != 0 {
		t.Fatalf("a full host took the figures: %+v", hosts[1])
	}
	b.record("omarchy", false, podman.Load{}, now.Add(time.Minute))
	hosts = []contract.HostSummary{{Name: "omarchy", ExecOnly: true}}
	if err := b.HostLoad(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	if hosts[0].Reachable || hosts[0].CPUs != 8 || hosts[0].LastSeen.IsZero() {
		t.Fatalf("unreachable keeps its last figures: %+v", hosts[0])
	}
}

// An exec-only host's disk stays out of the store's heartbeat, so warnings, eviction and placement never see it.
func TestReportHeartbeatKeepsExecOnlyDiskOutOfTheStore(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	records := &recordingHeartbeats{}
	b := &Boxes{HeartbeatStore: records, Pressure: engine.New(st, nil, nil, time.Now)}
	b.reportHeartbeat(context.Background(), "omarchy", true, podman.Load{ExecOnly: true, DiskBytes: 100, DiskUsed: 99}, time.Time{})
	if len(records.records) != 1 || records.records[0].free != nil || records.records[0].total != 0 || !records.records[0].ok {
		t.Fatalf("records %+v", records.records)
	}
}

func TestReportHeartbeatStoresGPUsOnlyWhenProbed(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	records := &recordingHeartbeats{}
	b := &Boxes{HeartbeatStore: records, Pressure: engine.New(st, nil, nil, time.Now)}
	cards := []contract.GPU{{Index: 0, Name: "RTX", MemBytes: 1 << 30}}
	none := []contract.GPU{}
	b.reportHeartbeat(context.Background(), "host", true, podman.Load{}, time.Time{})
	b.reportHeartbeat(context.Background(), "host", true, podman.Load{GPUs: &cards}, time.Time{})
	b.reportHeartbeat(context.Background(), "host", true, podman.Load{GPUs: &none}, time.Time{})
	b.reportHeartbeat(context.Background(), "host", false, podman.Load{GPUs: &cards}, time.Time{})
	if !reflect.DeepEqual(records.gpus, [][]contract.GPU{cards, none}) {
		t.Fatalf("stored GPUs %+v, want the two probes of a reachable host", records.gpus)
	}
}
