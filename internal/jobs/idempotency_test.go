package jobs

import (
	"testing"

	"dyna-agent/internal/machines"
)

func TestSubmitIdempotencyKeyReturnsOriginal(t *testing.T) {
	dir := t.TempDir()
	service := &Service{
		Store: Store{DataDir: dir}, DataDir: dir,
		Machines: fakeMachines{local: machines.Machine{Name: "local", Enabled: true}},
	}
	input := Input{Command: []string{"true"}, IdempotencyKey: "j2:test-invocation"}
	first, err := service.Submit(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.IdempotencyKey != input.IdempotencyKey {
		t.Fatalf("idempotent retry = %+v, want original %+v", second, first)
	}
	all, err := service.Store.List(t.Context(), false)
	if err != nil || len(all) != 1 {
		t.Fatalf("stored jobs = %+v, %v; want only the original", all, err)
	}
}
