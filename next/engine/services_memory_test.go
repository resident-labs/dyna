package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestReviewServiceUpdateMustHonorMeasuredHostMemory(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	spec := contract.ServiceSpec{Name: "web", Image: svcDigest, Host: "h"}
	if _, e := setService(r, spec); e != nil {
		t.Fatal(e)
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE services SET host='h'"); return err }); err != nil {
		t.Fatal(err)
	}
	r.s.reconciler.SetHostMemory(func(contract.HostName) int64 { return 128 << 30 }) // 100% of host memory, above the 80% gate
	spec.Memory = "1GiB"
	if _, e := setService(r, spec); e == nil {
		t.Fatal("service update admitted additional memory although current measured host memory is above the admission threshold")
	}
}
