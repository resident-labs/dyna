package devicegrant

import (
	"reflect"
	"testing"

	"dyna-agent/internal/scope"
)

func TestPutAndDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("paired", "phone", []string{"operate", "read"}, false); err != nil {
		t.Fatal(err)
	}
	phone, ok, err := s.Get("paired", "phone")
	if err != nil || !ok || phone.Legacy || !reflect.DeepEqual(phone.Scopes, scope.Default()) {
		t.Fatalf("explicit grant = %+v ok=%v err=%v", phone, ok, err)
	}
	if err := s.Put("paired", "old", scope.All(), true); err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.Get("paired", "old")
	if err != nil || !ok || !old.Legacy || !reflect.DeepEqual(old.Scopes, scope.All()) {
		t.Fatalf("legacy grant = %+v ok=%v err=%v", old, ok, err)
	}
	if err := s.Delete("paired", []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.Get("paired", "old"); err != nil || ok {
		t.Fatalf("deleted grant remains: ok=%v err=%v", ok, err)
	}
	if scope.Label(old.Scopes, old.Legacy) != scope.LegacyFull {
		t.Fatalf("label = %q", scope.Label(old.Scopes, old.Legacy))
	}
}
