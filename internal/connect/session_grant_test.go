package connect

import (
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
)

func TestSaveSessionGrantWritesTheDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "connect"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{o: Options{Store: store}}
	if err := svc.saveSessionGrant("cs_new", scope.Default(), false); err != nil {
		t.Fatal(err)
	}
	grants, err := devicegrant.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := grants.Get("connect", "cs_new")
	if err != nil || !ok || got.Legacy || !reflect.DeepEqual(got.Scopes, scope.Default()) {
		t.Fatalf("grant = %+v ok=%v err=%v", got, ok, err)
	}
}
