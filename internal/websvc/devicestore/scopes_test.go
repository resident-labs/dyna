package devicestore

import (
	"reflect"
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
	list, err := s.Devices()
	if err != nil || len(list) != 1 || list[0].Scopes != nil {
		t.Fatalf("stored devices = %+v %v", list, err)
	}
}
