package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.BrowserPath != "" || v.BrowserExtraFlags != "" || !v.BrowserUseFlagsFile || v.BrowserProfileMode != "shared" || v.BrowserOwnerProfile != "Default" || !v.BrowserSaveBack {
		t.Fatalf("defaults: %#v", v)
	}
	values := map[string]string{
		KeyBrowserPath: "/opt/chromium", KeyBrowserExtraFlags: "--lang=en --window-size=900,700",
		KeyBrowserUseFlagsFile: "false", KeyBrowserProfileMode: "copy-mine",
		KeyBrowserOwnerProfile: "Profile 2", KeyBrowserSaveBack: "false",
	}
	for k, value := range values {
		if err := v.Set(k, value); err != nil {
			t.Fatal(err)
		}
	}
	v.Extra["browser"] = map[string]any{"future": true}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.BrowserPath != v.BrowserPath || got.BrowserExtraFlags != v.BrowserExtraFlags || got.BrowserUseFlagsFile || got.BrowserProfileMode != "copy-mine" || got.BrowserOwnerProfile != "Profile 2" || got.BrowserSaveBack {
		t.Fatalf("round trip: %#v", got)
	}
	for k := range values {
		if got.Source(k) != SourceFile {
			t.Errorf("%s source = %s", k, got.Source(k))
		}
	}
	if got.Extra["browser"].(map[string]any)["future"] != true {
		t.Fatal("unknown browser key lost")
	}
}

func TestBrowserValidation(t *testing.T) {
	for _, tc := range [][2]string{
		{KeyBrowserProfileMode, "other"}, {KeyBrowserOwnerProfile, ""}, {KeyBrowserOwnerProfile, ".."},
		{KeyBrowserOwnerProfile, "../Default"}, {KeyBrowserOwnerProfile, "/Default"},
		{KeyBrowserOwnerProfile, "a\\b"}, {KeyBrowserOwnerProfile, "a\x00b"},
		{KeyBrowserUseFlagsFile, "maybe"}, {KeyBrowserSaveBack, "maybe"},
	} {
		v := Defaults()
		if err := v.Set(tc[0], tc[1]); err == nil {
			t.Errorf("accepted %q = %q", tc[0], tc[1])
		}
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, raw := range []string{
		`{"browser":{"profileMode":"other"}}`, `{"browser":{"ownerProfile":"../Default"}}`,
		`{"browser":{"saveBack":"true"}}`, `{"browser":{"path":5}}`, `{"browser":false}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
