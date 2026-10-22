package examples

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbedMatchesHandMaintainedJS(t *testing.T) {
	onDisk, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{}
	for _, entry := range onDisk {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".js" {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		want[entry.Name()] = data
	}
	if len(want) == 0 {
		t.Fatal("examples/ has no hand-maintained .js starters")
	}

	embedded, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, entry := range embedded {
		if entry.IsDir() {
			continue
		}
		data, err := FS.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		got[entry.Name()] = data
	}
	if len(got) != len(want) {
		t.Fatalf("embedded %d files, disk has %d", len(got), len(want))
	}
	for name, data := range want {
		if string(got[name]) != string(data) {
			t.Fatalf("embedded %s does not match the hand-maintained file", name)
		}
	}
}
