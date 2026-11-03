package images

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	first, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"}))
	if err != nil || same.ID != first.ID {
		t.Fatalf("same folder: %v %v", same.ID, err)
	}
	changed, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n", "setup.sh": "x"}))
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
		"ci image from the box base": {contract.CIRunnerImage, "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"},
		"box image from the ci base": {"rust", "ARG DYNA_CI_BASE\nFROM ${DYNA_CI_BASE}\n"},
		"a foreign base":             {"rust", "FROM ubuntu:24.04\n"},
		"bad name":                   {"Rust Box", "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"},
	} {
		if _, err := store.Save("room_a", c.image, pack(t, map[string]string{"Containerfile": c.recipe})); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"setup.sh": "x"})); !errors.Is(err, ErrInvalid) {
		t.Errorf("no Containerfile: got %v", err)
	}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n", "box-needs": "root\n"})); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown need: got %v", err)
	}
	if _, err := store.Save("room_a", contract.CIRunnerImage, pack(t, map[string]string{"Containerfile": "ARG DYNA_CI_BASE\nFROM ${DYNA_CI_BASE}\nRUN true\n"})); err != nil {
		t.Errorf("ci-runner: %v", err)
	}
}

func TestListShowRemove(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\nRUN true", "a/b.sh": "x"})); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.List("room_a"); len(list) != 1 || list[0].Files != 2 {
		t.Fatalf("list %+v", list)
	}
	if other, _ := store.List("room_b"); len(other) != 0 {
		t.Fatalf("another room sees %+v", other)
	}
	_, recipe, files, err := store.Show("room_a", "rust")
	if err != nil || recipe != "ARG DYNA_BASE\nFROM ${DYNA_BASE}\nRUN true" || len(files) != 2 || files[0] != "Containerfile" {
		t.Fatalf("show %q %v %v", recipe, files, err)
	}
	if err := store.Remove("room_a", "rust"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("room_a", "rust"); !errors.Is(err, ErrMissing) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestSaveHoldsEveryFromToTheBase(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	for name, recipe := range map[string]string{
		"a second FROM":         "ARG DYNA_BASE\nFROM ${DYNA_BASE}\nFROM ubuntu\n",
		"a first FROM":          "ARG DYNA_BASE\nFROM ubuntu\nFROM ${DYNA_BASE}\n",
		"FROM before the base":  "FROM ubuntu\nARG DYNA_BASE\nFROM ${DYNA_BASE}\n",
		"a joined line":         "ARG DYNA_BASE\nFROM ubuntu\nRUN echo \\\nFROM ${DYNA_BASE}\n",
		"lowercase from":        "ARG DYNA_BASE\nfrom ubuntu\n",
		"a stage from a stage":  "ARG DYNA_BASE\nFROM ${DYNA_BASE} AS a\nFROM a\n",
		"no ARG":                "FROM ${DYNA_BASE}\n",
		"a platform flag":       "ARG DYNA_BASE\nFROM --platform=linux/arm64 ${DYNA_BASE}\n",
		"RUN before the first":  "RUN true\nARG DYNA_BASE\nFROM ${DYNA_BASE}\n",
		"a commented base only": "ARG DYNA_BASE\n# FROM ${DYNA_BASE}\nFROM ubuntu\n",
	} {
		if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": recipe})); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	for name, recipe := range map[string]string{
		"lowercase and a comment": "# note\narg DYNA_BASE\n\nfrom ${DYNA_BASE}\nrun true\n",
		"a bare variable":         "ARG DYNA_BASE\nFROM $DYNA_BASE\n",
		"stages from the base":    "ARG DYNA_BASE\nFROM ${DYNA_BASE} AS build\nRUN true\nFROM ${DYNA_BASE}\nCOPY --from=build /a /a\n",
		"a continued RUN":         "ARG DYNA_BASE\nFROM ${DYNA_BASE}\nRUN apt-get update \\\n  && true\n",
	} {
		if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": recipe})); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNamesNeverLeaveTheRoomsFolder(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	if _, err := store.Save("room_b", "secret", pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"})); err != nil {
		t.Fatal(err)
	}
	room, _ := filepath.Rel(store.Layout.Images("room_a"), store.Layout.Images("room_b"))
	for _, name := range []string{"../../" + "room_b/images/secret", room + "/secret", "..", ".", "", "/etc", "a/b"} {
		if _, _, _, err := store.Show("room_a", name); !errors.Is(err, ErrMissing) {
			t.Errorf("show %q: %v", name, err)
		}
		if err := store.Remove("room_a", name); !errors.Is(err, ErrMissing) {
			t.Errorf("remove %q: %v", name, err)
		}
		if _, err := store.Save("room_a", name, pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"})); !errors.Is(err, ErrInvalid) {
			t.Errorf("save %q: %v", name, err)
		}
	}
	if list, _ := store.List("room_b"); len(list) != 1 {
		t.Fatalf("room_b lost its image: %+v", list)
	}
}

func TestSaveLimitsWhatARoomHolds(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	recipe := "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"
	if _, err := store.Save("room_a", "big", pack(t, map[string]string{"Containerfile": recipe, "blob": string(noise(skills.MaxPacked + 1))})); !errors.Is(err, ErrInvalid) {
		t.Errorf("over the packed limit: %v", err)
	}
	for i := range MaxImagesPerRoom {
		if _, err := store.Save("room_a", fmt.Sprintf("i%d", i), pack(t, map[string]string{"Containerfile": recipe})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Save("room_a", "one-too-many", pack(t, map[string]string{"Containerfile": recipe})); !errors.Is(err, ErrInvalid) {
		t.Errorf("over the count: %v", err)
	}
	if _, err := store.Save("room_a", "i0", pack(t, map[string]string{"Containerfile": recipe, "a": "b"})); err != nil {
		t.Errorf("replacing one at the limit: %v", err)
	}
	if _, err := store.Save("room_b", "i0", pack(t, map[string]string{"Containerfile": recipe})); err != nil {
		t.Errorf("another room has its own: %v", err)
	}
}

func TestACrashedSaveLeavesTheOldImage(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	recipe := "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": recipe})); err != nil {
		t.Fatal(err)
	}
	dir := store.Layout.Images("room_a")
	// A crash after the old image moved aside, and a half-unpacked save.
	if err := os.Rename(filepath.Join(dir, "rust"), filepath.Join(dir, ".old-rust")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".save-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("room_a", "other", pack(t, map[string]string{"Containerfile": recipe})); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.List("room_a"); len(list) != 2 {
		t.Fatalf("the old image did not come back: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(dir, ".save-123")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover save kept: %v", err)
	}
}

// noise does not compress, so an archive of it packs to about its size.
func noise(n int) []byte {
	data := make([]byte, n)
	_, _ = rand.Read(data)
	return data
}

func TestRecipeParserCannotBeSidestepped(t *testing.T) {
	store := &Store{Layout: box.Layout{Data: t.TempDir()}}
	head := "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"
	for name, recipe := range map[string]string{
		"a backslash at the end":    head + "FROM ubuntu \\",
		"a syntax directive":        "# syntax=evil/frontend\n" + head,
		"an escape directive":       "# escape=`\n" + head + "RUN true \\\nFROM ubuntu\n",
		"a check directive":         "#check=skip=all\n" + head,
		"a directive after another": "# syntax=a/b\n# escape=`\n" + head,
	} {
		if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": recipe})); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// Only a backslash on a line: no panic, and the file is fine.
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": head + "\\\n\n"})); err != nil {
		t.Errorf("lone backslash: %v", err)
	}
	// A comment that is not a directive is fine, as is one after the first line.
	if _, err := store.Save("room_a", "rust", pack(t, map[string]string{"Containerfile": "# a note\n" + head + "# escape=x\nRUN true\n"})); err != nil {
		t.Errorf("comments: %v", err)
	}
}

func TestAnImageMovedAsideByACrashComesBackOnEveryRead(t *testing.T) {
	recipe := pack(t, map[string]string{"Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"})
	for name, read := range map[string]func(*Store) error{
		"list":   func(s *Store) error { _, err := s.List("room_a"); return err },
		"show":   func(s *Store) error { _, _, _, err := s.Show("room_a", "x"); return err },
		"remove": func(s *Store) error { return s.Remove("room_a", "x") },
	} {
		store := &Store{Layout: box.Layout{Data: t.TempDir()}}
		if _, err := store.Save("room_a", "x", recipe); err != nil {
			t.Fatal(err)
		}
		dir := store.Layout.Images("room_a")
		if err := os.Rename(filepath.Join(dir, "x"), filepath.Join(dir, ".old-x")); err != nil {
			t.Fatal(err)
		}
		if err := read(store); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// After the read the image is back or, for remove, gone for good: a later save must not resurrect it.
		if _, err := store.Save("room_a", "y", recipe); err != nil {
			t.Fatal(err)
		}
		list, _ := store.List("room_a")
		want := 2
		if name == "remove" {
			want = 1
		}
		if len(list) != want {
			t.Errorf("%s: %+v", name, list)
		}
	}
}
