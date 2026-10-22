package settings

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalLaneRepositoriesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	if e := v.Set(KeyLocalLaneRepos, `["/repo one","/repo-two"]`); e != nil {
		t.Fatal(e)
	}
	if e := v.Save(); e != nil {
		t.Fatal(e)
	}
	got, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got.LocalLaneRepos, []string{"/repo one", "/repo-two"}) || got.Source(KeyLocalLaneRepos) != SourceFile {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []string{`"repo"`, `["relative"]`, `[1]`} {
		if e := v.Set(KeyLocalLaneRepos, bad); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
