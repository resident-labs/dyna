package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil || v.RootProfile != "opus" || v.Source(KeyRootProfile) != SourceDefault {
		t.Fatalf("defaults = %+v, %v", v, err)
	}
	for _, name := range []string{" chosen ", "missing", ""} {
		if err = v.Set(KeyRootProfile, name); err != nil {
			t.Fatal(err)
		}
		want := v.RootProfile
		if err = v.Save(); err != nil {
			t.Fatal(err)
		}
		v, err = Load(path)
		if err != nil || v.RootProfile != want || v.Source(KeyRootProfile) != SourceFile {
			t.Fatalf("round trip = %+v, %v", v, err)
		}
	}
	for _, raw := range []string{`{"root":false}`, `{"root":{"profile":42}}`} {
		if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(path); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
