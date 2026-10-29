package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidOpenRecordNamesSupplyingIndex(t *testing.T) {
	root := newRoot(t)
	dir := filepath.Dir(root)
	if err := os.MkdirAll(filepath.Join(root, "closed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "records"), 0700); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "index.json")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(index, `{"version":3,"recordFiles":{"open":"../bad.json"},"closedIndex":"index.json"}`)
	write(filepath.Join(root, "closed", "index.json"), `{}`)
	if err := Migrate(dir); err == nil || !strings.Contains(err.Error(), index) {
		t.Fatalf("missing supplying index: %v", err)
	}
	write(index, `{"version":3,"recordFiles":{"open":"open.json"},"closedIndex":"index.json"}`)
	body := filepath.Join(root, "records", "open.json")
	write(body, `{`)
	if err := Migrate(dir); err == nil || !strings.Contains(err.Error(), body) {
		t.Fatalf("missing record source: %v", err)
	}
}
