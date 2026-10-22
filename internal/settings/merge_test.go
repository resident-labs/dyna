package settings

import (
	"path/filepath"
	"testing"
)

func TestMergeStewardProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.MergeStewardProfile != "" {
		t.Fatal("steward should use Auto")
	}
	if err := v.Set(KeyMergeStewardProfile, " sol "); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil || saved.MergeStewardProfile != "sol" {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	if err := saved.Set(KeyMergeStewardProfile, ""); err != nil || saved.MergeStewardProfile != "" {
		t.Fatal("cannot reset Auto", err)
	}
}

func TestMergeStewardProfileOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"off", "Off", " OFF "} {
		if err := v.Set(KeyMergeStewardProfile, input); err != nil {
			t.Fatal(err)
		}
		if v.MergeStewardProfile != "off" {
			t.Fatalf("Set(%q) stored %q, want off", input, v.MergeStewardProfile)
		}
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil || saved.MergeStewardProfile != "off" {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	if err := saved.Set(KeyMergeStewardProfile, ""); err != nil || saved.MergeStewardProfile != "" {
		t.Fatal("cannot reset Auto", err)
	}
}
