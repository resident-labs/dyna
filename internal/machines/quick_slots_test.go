package machines

import (
	"context"
	"testing"
)

func TestQuickSlotsPersistAndDefault(t *testing.T) {
	ctx := context.Background()
	store := Store{DataDir: t.TempDir()}
	node, err := store.Save(ctx, Machine{Name: "quick-node", SSH: "host", Enabled: true, QuickSlots: 7})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, node.Name)
	if err != nil || got.QuickSlotLimit() != 7 {
		t.Fatalf("node = %+v, error = %v", got, err)
	}
	got.QuickSlots = 0
	got.Capabilities.CPUs = 32
	if got.QuickSlotLimit() != 8 {
		t.Fatalf("default = %d", got.QuickSlotLimit())
	}
	got.Capabilities.CPUs = 2
	if got.QuickSlotLimit() != 4 {
		t.Fatalf("minimum = %d", got.QuickSlotLimit())
	}
}
