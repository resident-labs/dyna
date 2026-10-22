package subagent

import (
	"os"
	"testing"
)

func TestLegacyServiceSessionAndHelperFlags(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	for _, role := range []string{"reviewer", "keeper"} {
		parent := Meta{ID: NewID(), RoomID: "room", Room: "room", Role: role}
		child := Meta{ID: NewID(), RoomID: "room", Room: "room", ParentSession: parent.ID}
		for _, meta := range []Meta{parent, child} {
			dir, _ := store.Dir(meta.ID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := store.writeMeta(meta); err != nil {
				t.Fatal(err)
			}
			got, err := store.ReadMeta(meta.ID)
			if err != nil || !got.Service {
				t.Fatalf("legacy %s: %+v %v", role, got, err)
			}
		}
	}
	t.Run("cycle", func(t *testing.T) {
		meta := Meta{ID: NewID(), RoomID: "room", Room: "room"}
		meta.ParentSession = meta.ID
		dir, _ := store.Dir(meta.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := store.writeMeta(meta); err != nil {
			t.Fatal(err)
		}
		got, err := store.ReadMeta(meta.ID)
		if err != nil || got.Service {
			t.Fatalf("cyclic ordinary session: %+v %v", got, err)
		}
	})
}
