package settings

import (
	"path/filepath"
	"testing"
)

func TestShellSilenceSettingRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil || v.ShellSilenceMinutes != 30 {
		t.Fatalf("default = %d, %v", v.ShellSilenceMinutes, err)
	}
	if err := v.Set(KeyShellSilenceMinutes, "-1"); err == nil {
		t.Fatal("negative threshold accepted")
	}
	if err := v.Set(KeyShellSilenceMinutes, "0"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	v, err = Load(path)
	if err != nil || v.ShellSilenceMinutes != 0 {
		t.Fatalf("saved = %d, %v", v.ShellSilenceMinutes, err)
	}
}
