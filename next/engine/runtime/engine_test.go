package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"

	"github.com/resident-labs/dyna/contract"
)

func TestPiPackageInstalledBesideBinary(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "dyna")
	path := filepath.Join(dir, "pi.tgz")
	if _, err := loadPi(executable); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing package error: %v", err)
	}
	if err := os.WriteFile(path, []byte("pi"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := loadPi(executable)
	if err != nil || string(data) != "pi" {
		t.Fatalf("package %q, error %v", data, err)
	}
}

type fakeRegistrar struct{ called bool }

func (f *fakeRegistrar) AddHost(context.Context, contract.HostAddRequest) error {
	f.called = true
	return nil
}

func (*fakeRegistrar) HostLoad(context.Context, []contract.HostSummary) error { return nil }

func TestHostAddRequiresOwnerAndConfirmation(t *testing.T) {
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	_, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host"})
	if err == nil || err.Code != contract.ErrInvalid || f.called {
		t.Fatalf("error=%v called=%v", err, f.called)
	}
}

type listStore struct{ boxStore }

func (listStore) Boxes(context.Context) ([]box.Box, error) {
	return []box.Box{{Host: "host", State: contract.BoxReady}, {Host: "host", State: contract.BoxStopped}}, nil
}
func TestHostListIncludesLoadAndRunningBoxes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AddHost(ctx, box.Host{Name: "host", CPUs: 8, MemBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: listStore{}, loads: map[contract.HostName]contract.HostSummary{"host": {Reachable: true, CPUUsed: 1.5, MemUsed: 512}}}
	svc := serviceWithHosts{Service: engine.New(st, nil, nil, time.Now), boxes: b}
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	want := contract.HostSummary{Name: "host", Reachable: true, Boxes: 1, CPUs: 8, MemBytes: 4096, CPUUsed: 1.5, MemUsed: 512}
	if problem != nil || len(got.Hosts) != 2 || got.Hosts[1] != want {
		t.Fatalf("hosts: %+v, error: %v", got, problem)
	}
}
