package settings

import (
	"path/filepath"
	"testing"
)

func TestLibrarySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil || v.LibraryOn("/src/app") || v.LibraryProfile != "" {
		t.Fatal(v, err)
	}
	if err := v.Set(KeyLibraryEnabled, `["relative"]`); err == nil {
		t.Fatal("relative repository path accepted")
	}
	if err := v.Set(KeyLibraryEnabled, `["/src/app"]`); err != nil {
		t.Fatal(err)
	}
	if v.LibraryReadmeMaxLines != DefaultLibraryReadmeMaxLines {
		t.Fatalf("readme default %d", v.LibraryReadmeMaxLines)
	}
	if err := v.Set(KeyLibraryReadmeMaxLines, "0"); err == nil {
		t.Fatal("zero readme limit accepted")
	}
	if err := v.Set(KeyLibraryReadmeMaxLines, "120"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(KeyLibraryProfile, " cheap "); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(KeyLibrarySmellProfile, " diagnostic "); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if v, err = Load(path); err != nil || !v.LibraryOn("/src/app/") || v.LibraryOn("/src/other") || v.LibraryProfile != "cheap" || v.LibrarySmellProfile != "diagnostic" || v.LibraryReadmeMaxLines != 120 {
		t.Fatal(v, err)
	}
}
