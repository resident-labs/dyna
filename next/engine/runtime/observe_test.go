package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		forward := box.Forward{Box: tc.id, HostPath: "/host/" + string(tc.id), EnginePath: "/engine/" + string(tc.id)}
		status, present, err := inspectContainer(context.Background(), h, forward)
		if err != nil || present != tc.present || (present && (status.Box != tc.id || status.Running != tc.running)) {
			t.Fatalf("inspect %s: %+v present=%v err=%v", tc.id, status, present, err)
		}
	}
	if len(h.Calls) != 4 || h.Calls[1] != fakehost.AddForward || len(h.Forwards) != 1 {
		t.Fatalf("host calls=%v forwards=%v, want one list per inspection and a running-box forward", h.Calls, h.Forwards)
	}
	h.FailNext = errors.New("offline")
	if _, _, err := inspectContainer(context.Background(), h, box.Forward{Box: "run"}); err == nil {
		t.Fatal("host error was ignored")
	}
}

type heartbeatRecord struct {
	ok   bool
	free *int64
}
type recordingHeartbeats struct{ records []heartbeatRecord }

func (r *recordingHeartbeats) RecordHostHeartbeat(_ context.Context, _ contract.HostName, ok bool, free *int64, _ time.Time) error {
	r.records = append(r.records, heartbeatRecord{ok: ok, free: free})
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
	if len(records.records) != 3 || records.records[0].free == nil || *records.records[0].free != 70 || records.records[1].free != nil || records.records[2].free != nil || records.records[2].ok {
		t.Fatalf("heartbeat records=%+v", records.records)
	}
}

type offlineRestoreStore struct{ boxStore }

func (offlineRestoreStore) Boxes(context.Context) ([]box.Box, error) {
	return []box.Box{{ID: "b", Session: "s", Host: "h", State: contract.BoxReady}}, nil
}
func (offlineRestoreStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "r"}, nil
}

type offlineRunner struct{}

func (offlineRunner) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("host offline")
}

func TestRestoreToleratesOfflineHost(t *testing.T) {
	runtimeDir := t.TempDir()
	host, err := podman.New("user@host", runtimeDir, "h", offlineRunner{})
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
	b := &Boxes{Store: offlineRestoreStore{}, Sockets: sockets, Runtime: runtimeDir, hosts: map[contract.HostName]*podman.Host{"h": host}}
	b.binOnce.Do(func() { b.binSum = "sum" })
	if err := b.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "boxes", "b.sock")); err != nil {
		t.Fatalf("local box socket: %v", err)
	}
}
