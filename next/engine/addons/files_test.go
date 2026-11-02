package addons

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

func archive(t *testing.T, name string) []byte {
	t.Helper()
	a := skills.NewArchive()
	if err := a.Add(skills.File, 0o600, []byte("---\nname: "+name+"\ndescription: d\n---\n")); err != nil {
		t.Fatal(err)
	}
	if err := a.Add("notes/more.txt", 0o600, []byte(name)); err != nil {
		t.Fatal(err)
	}
	data, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func bundleItems(t *testing.T, f *Files, room contract.RoomID) ([]string, string) {
	t.Helper()
	data, err := f.Bundle(room)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := skills.Extract(data, dir); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, dir
}

func TestAScopeKeepsItsItemsInItsOwnFolder(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	f := &Files{Layout: layout}
	for _, room := range []contract.RoomID{"", "room"} {
		if data, err := f.Bundle(room); err != nil || data != nil {
			t.Fatalf("a scope that never held anything bundled %d bytes: %v", len(data), err)
		}
	}
	item := contract.Addon{Kind: contract.AddonPackage, Name: "tools", Source: "npm:tools@1.0.0", By: "owner"}
	if err := f.Put("room", item); err != nil {
		t.Fatal(err)
	}
	item.Source = "npm:tools@2.0.0"
	if err := f.Put("room", item); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveSkill("room", contract.Addon{Kind: contract.AddonSkill, Name: "pdf"}, archive(t, "pdf")); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveSkill("", contract.Addon{Kind: contract.AddonSkill, Name: "pdf"}, archive(t, "pdf")); err != nil {
		t.Fatal(err)
	}
	items, err := f.List("room")
	if err != nil || len(items) != 2 || items[0].Room != "room" || items[0].Source != "npm:tools@2.0.0" {
		t.Fatalf("%+v %v", items, err)
	}
	for _, path := range []string{layout.Addons("room"), filepath.Join(layout.Skills("room"), "pdf", "notes", "more.txt"), layout.Addons(""), filepath.Join(layout.Skills(""), "pdf", skills.File)} {
		if _, err := os.Stat(path); err != nil {
			t.Error(err)
		}
	}
	// Deleting the room's folder takes its add-ons and leaves the global ones.
	folder, err := layout.Room("room")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	if items, err := f.List("room"); err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
	if found, err := f.Has("", contract.AddonSkill, "pdf"); err != nil || !found {
		t.Fatal(found, err)
	}
}

func TestABundleHoldsWhatIsEnabled(t *testing.T) {
	f := &Files{Layout: box.Layout{Data: t.TempDir()}}
	for _, name := range []string{"one", "two"} {
		if err := f.SaveSkill("", contract.Addon{Kind: contract.AddonSkill, Name: name}, archive(t, name)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.Bundle("")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.Bundle(""); err != nil || string(again) != string(first) {
		t.Fatal("the same scope bundled differently", err)
	}
	if err := f.SetEnabled("", contract.AddonSkill, "two", false); err != nil {
		t.Fatal(err)
	}
	if names, _ := bundleItems(t, f, ""); !slices.Equal(names, []string{"addons.json", "one"}) {
		t.Fatal(names)
	}
	if err := f.SetEnabled("", contract.AddonSkill, "two", true); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("", contract.AddonSkill, "one"); err != nil {
		t.Fatal(err)
	}
	if names, _ := bundleItems(t, f, ""); !slices.Equal(names, []string{"addons.json", "two"}) {
		t.Fatal(names)
	}
	if _, err := os.Stat(filepath.Join(f.Layout.Skills(""), "one")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// Emptied, a scope still bundles, so a host's old copy is replaced.
	if err := f.Remove("", contract.AddonSkill, "two"); err != nil {
		t.Fatal(err)
	}
	if names, _ := bundleItems(t, f, ""); !slices.Equal(names, []string{"addons.json"}) {
		t.Fatal(names)
	}
	if err := f.Remove("", contract.AddonSkill, "two"); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	if err := f.SetEnabled("", contract.AddonMCP, "none", true); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}
