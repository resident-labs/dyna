package browser

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestShouldExcludeAndPackUnpackRoundtrip(t *testing.T) {
	src := t.TempDir()
	kept := map[string]string{
		"Local State":         `{"os_crypt":{}}`,
		"Default/Cookies":     "session=abc123",
		"Default/Preferences": `{"profile":{}}`,
		"Default/Local Storage/leveldb/000003.log":   "ls-data",
		"Default/IndexedDB/https_example.com_0/data": "idb-data",
		"Default/Service Worker/Database/MANIFEST":   "sw-db",
		"Profile 1/Local Storage/data":               "p1-ls",
		"Profile 1/IndexedDB/data":                   "p1-idb",
		"Profile 1/Service Worker/Database/0001":     "p1-sw-db",
	}
	excluded := []string{
		"Crashpad/reports/crash.dmp",
		"Default/Cache/Cache_Data/f_000001",
		"Default/Code Cache/js/abc",
		"Default/GPUCache/data_0",
		"Default/ShaderCache/s0",
		"Default/GrShaderCache/g0",
		"Default/DawnCache/d0",
		"Default/Service Worker/CacheStorage/entry",
		"Default/Service Worker/ScriptCache/script",
		"Profile 1/Cache/f_001",
		"Profile 1/Service Worker/CacheStorage/c0",
		"ShaderCache/root_shader",
		"GrShaderCache/root_gr",
		"DawnCache/root_dawn",
		"SingletonLock",
		"SingletonCookie",
		"SingletonSocket",
		"Default/SingletonLock",
	}
	for rel, content := range kept {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range excluded {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("junk"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Relative in-profile symlink should survive.
	if err := os.Symlink("Cookies", filepath.Join(src, "Default", "CookiesLink")); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	if err := PackProfile(src, &archive); err != nil {
		t.Fatalf("PackProfile: %v", err)
	}

	// Verify tar entries directly.
	tr := tar.NewReader(bytes.NewReader(archive.Bytes()))
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		seen[hdr.Name] = true
	}
	for _, rel := range excluded {
		if seen[rel] {
			t.Errorf("tar unexpectedly included excluded path %q", rel)
		}
	}
	for rel := range kept {
		if !seen[rel] {
			t.Errorf("tar omitted kept path %q", rel)
		}
	}

	dst := t.TempDir()
	if err := UnpackProfile(bytes.NewReader(archive.Bytes()), dst); err != nil {
		t.Fatalf("UnpackProfile: %v", err)
	}
	for rel, want := range kept {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("missing unpacked %q: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("unpacked %q = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range excluded {
		if _, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("excluded path %q should not be unpacked (err=%v)", rel, err)
		}
	}
	target, err := os.Readlink(filepath.Join(dst, "Default", "CookiesLink"))
	if err != nil || target != "Cookies" {
		t.Fatalf("symlink = %q %v, want Cookies", target, err)
	}
}

func TestUnpackRejectsTraversalAndEscapingSymlinks(t *testing.T) {
	makeTar := func(hdr *tar.Header, body string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	cases := []struct {
		name string
		hdr  *tar.Header
		body string
	}{
		{"parent traversal", &tar.Header{Name: "../escape", Mode: 0600, Typeflag: tar.TypeReg}, "bad"},
		{"nested traversal", &tar.Header{Name: "Default/../../escape", Mode: 0600, Typeflag: tar.TypeReg}, "bad"},
		{"absolute path", &tar.Header{Name: "/tmp/escape", Mode: 0600, Typeflag: tar.TypeReg}, "bad"},
		{"escaping symlink", &tar.Header{Name: "Default/link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"}, ""},
		{"absolute symlink", &tar.Header{Name: "Default/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := makeTar(tc.hdr, tc.body)
			if err := UnpackProfile(bytes.NewReader(raw), t.TempDir()); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestValidBrowserNameAndLayout(t *testing.T) {
	valid := []string{"personal", "work-1", "a_b", "0profile"}
	for _, name := range valid {
		if !box.ValidBrowserName(name) {
			t.Errorf("ValidBrowserName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "none", "None", "NONE", "-bad", "_bad", "../bad", "a/b", "a.b", "has space"}
	for _, name := range invalid {
		if box.ValidBrowserName(name) {
			t.Errorf("ValidBrowserName(%q) = true, want false", name)
		}
	}
	l := box.Layout{Data: "/data"}
	got, err := l.Browser("personal")
	if err != nil || got != "/data/browsers/personal" {
		t.Fatalf("Browser = %q %v", got, err)
	}
	prev, err := l.BrowserPrev("personal")
	if err != nil || prev != "/data/browsers/personal.prev" {
		t.Fatalf("BrowserPrev = %q %v", prev, err)
	}
	if _, err := l.Browser("../bad"); err == nil {
		t.Fatal("expected error for invalid name")
	}
}
