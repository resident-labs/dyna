package subagent

import (
	"os"
	"testing"
)

func TestStoredServiceSessionAndHelperFlags(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	for _, role := range []string{"reviewer", RoleLibrarian} {
		parent := Meta{ID: NewID(), RoomID: "room", Room: "room", Role: role, Service: true}
		child := Meta{ID: NewID(), RoomID: "room", Room: "room", ParentSession: parent.ID, Service: true}
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
				t.Fatalf("stored %s: %+v %v", role, got, err)
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
