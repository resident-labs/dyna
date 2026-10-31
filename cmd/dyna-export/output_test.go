package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestWriteBundleRequiresSafeManifestAndPrivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundle")
	global := contract.GlobalBundle{Rooms: []contract.RoomName{"../unsafe"}}
	if err := writeBundle(dir, global, []contract.RoomBundle{{Name: "../unsafe"}}); err == nil {
		t.Fatal("accepted unsafe room name")
	}
	global.Rooms = []contract.RoomName{"alpha"}
	if err := writeBundle(dir, global, []contract.RoomBundle{{Name: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"global.json", "rooms/alpha.json"} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s has mode %o", rel, info.Mode().Perm())
		}
	}
}
