package images

import (
	"errors"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

func pack(t *testing.T, files map[string]string) []byte {
	t.Helper()
	archive := skills.NewArchive()
	for name, data := range files {
		if err := archive.Add(name, 0o600, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	packed, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return packed
}

func TestSaveChangesTheIDOnlyWhenTheFolderChanges(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	first, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "FROM ${DYNA_BASE}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "FROM ${DYNA_BASE}\n"}))
	if err != nil || same.ID != first.ID {
		t.Fatalf("same folder: %v %v", same.ID, err)
	}
	changed, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "FROM ${DYNA_BASE}\n", "setup.sh": "x"}))
	if err != nil || changed.ID == first.ID {
		t.Fatalf("changed folder kept id %v: %v", changed.ID, err)
	}
	if Tag("rust", "base", TreeID(first.ID)) == Tag("rust", "base", TreeID(changed.ID)) {
		t.Fatal("tag did not change")
	}
}

func TestSaveRefusesTheWrongBaseAndBadNames(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	for name, c := range map[string]struct{ image, recipe string }{
		"ci image from the box base": {contract.CIRunnerImage, "FROM ${DYNA_BASE}\n"},
		"box image from the ci base": {"rust", "FROM ${DYNA_CI_BASE}\n"},
		"a foreign base":             {"rust", "FROM ubuntu:24.04\n"},
		"bad name":                   {"Rust Box", "FROM ${DYNA_BASE}\n"},
	} {
		if _, err := store.Save("room_a", c.image, pack(t, map[string]string{"Containerfile": c.recipe})); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"setup.sh": "x"})); !errors.Is(err, ErrInvalid) {
		t.Errorf("no Containerfile: got %v", err)
	}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "FROM ${DYNA_BASE}\n", "box-needs": "root\n"})); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown need: got %v", err)
	}
	if _, err := store.Save("room_a", contract.CIRunnerImage, pack(t, map[string]string{"Containerfile": "FROM ${DYNA_CI_BASE}\nRUN true\n"})); err != nil {
		t.Errorf("ci-runner: %v", err)
	}
}

func TestListShowRemove(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "FROM ${DYNA_BASE}\nRUN true", "a/b.sh": "x"})); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.List("room_a"); len(list) != 1 || list[0].Files != 2 {
		t.Fatalf("list %+v", list)
	}
	if other, _ := store.List("room_b"); len(other) != 0 {
		t.Fatalf("another room sees %+v", other)
	}
	_, recipe, files, err := store.Show("room_a", "rust")
	if err != nil || recipe != "FROM ${DYNA_BASE}\nRUN true" || len(files) != 2 || files[0] != "Containerfile" {
		t.Fatalf("show %q %v %v", recipe, files, err)
	}
	if err := store.Remove("room_a", "rust"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("room_a", "rust"); !errors.Is(err, ErrMissing) {
		t.Fatalf("second remove: %v", err)
	}
}
