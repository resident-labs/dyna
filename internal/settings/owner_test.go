package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerProfileSavesValidValuesAndClearsTheSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]string{KeyOwnerName: strings.Repeat("a", MaxOwnerName+1), KeyOwnerAvatar: "ari"} {
		if err := v.Set(key, bad); err == nil {
			t.Errorf("%s accepted %q", key, bad)
		}
	}
	if err := v.Set(KeyOwnerName, " Ari "); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(KeyOwnerAvatar, "👩‍🚀"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.OwnerName != "Ari" || got.OwnerAvatar != "👩‍🚀" {
		t.Fatalf("loaded %q %q, %v", got.OwnerName, got.OwnerAvatar, err)
	}
	if err := got.Set(KeyOwnerName, ""); err != nil {
		t.Fatal(err)
	}
	if err := got.Set(KeyOwnerAvatar, ""); err != nil {
		t.Fatal(err)
	}
	if err := got.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"owner"`) {
		t.Fatalf("cleared profile left %s", b)
	}
}
