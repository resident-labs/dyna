package devicestore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScopedCodeReturnsScopesAndDoesNotStoreThemOnTheDevice(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.NewScopedCode("", []string{"files", "read"})
	if err != nil {
		t.Fatal(err)
	}
	_, dev, err := s.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dev.Scopes, []string{"files", "read"}) {
		t.Fatalf("scopes = %#v", dev.Scopes)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "files") || strings.Contains(string(raw), "scopes") {
		t.Fatalf("devices.json stored scopes: %s", raw)
	}
}
