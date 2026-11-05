package engine

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestHostListShowsEachCardWithTheVRAMRequestedOfIt(t *testing.T) {
	s, room, root, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.AddHost(ctx, box.Host{Name: "gpu", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	cards := []contract.GPU{{Index: 0, Name: "RTX 2080 Ti", MemBytes: 11 * box.GiB}, {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4 * box.GiB}}
	if err := s.store.SetHostGPUs(ctx, "gpu", cards); err != nil {
		t.Fatal(err)
	}
	for name, gpu := range map[string]contract.GPUGiB{"ready": 3, "stopped": 2} {
		started, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: root, Name: name, Profile: "test", Goal: "g", GPU: gpu})
		if problem != nil {
			t.Fatal(problem)
		}
		state := name
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE boxes SET host='gpu',gpu_index=1,state=? WHERE id=?", state, started.Box)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, problem := s.HostList(ctx, owner, contract.HostListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	var got []contract.GPU
	for _, h := range list.Hosts {
		if h.Name == "gpu" {
			got = h.GPUs
		}
	}
	want := []contract.GPU{cards[0], {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4 * box.GiB, RequestedBytes: 3 * box.GiB}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cards %+v, want %+v", got, want)
	}
}
