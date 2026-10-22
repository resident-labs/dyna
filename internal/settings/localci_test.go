package settings

import (
	"path/filepath"
	"testing"
)

func TestLocalCIRemoteRoundTrip(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v.LocalCIRemote != "ci@build-host:/var/tmp/dyna-local-ci" {
		t.Fatalf("default=%q", v.LocalCIRemote)
	}
	if err = v.Set(KeyLocalCIRemote, "ci@other:/srv/ci"); err != nil {
		t.Fatal(err)
	}
	if err = v.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(v.Path())
	if err != nil || loaded.LocalCIRemote != v.LocalCIRemote || loaded.Source(KeyLocalCIRemote) != SourceFile {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	for _, bad := range []string{"-oProxyCommand=bad:/tmp/ci", "host:relative", "host:/", "host:/tmp/ci;bad", "host name:/tmp/ci"} {
		if err = v.Set(KeyLocalCIRemote, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
