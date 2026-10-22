package browserconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnvironmentGating(t *testing.T) {
	for _, tc := range []struct {
		kernel, interop, distro, file, env string
		want                               bool
	}{
		{"Linux", "", "", "", "", false},
		{"6.6-microsoft-standard-WSL2", "", "", "", "", true},
		{"5.15-WSL", "", "", "", "", true},
		{"Linux", "/run/WSL/1", "", "", "", true},
		{"Linux", "", "Ubuntu", "", "", true},
		{"Linux", "", "", "systemd-nspawn\n", "", true},
		{"Linux", "", "", "", "systemd-nspawn", true},
		{"Linux", "", "", "nspawn", "", true},
		{"Linux", "", "", "docker", "docker", false},
	} {
		if got := basicPasswordStore(tc.kernel, tc.interop, tc.distro, tc.file, tc.env); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
	for _, key := range []string{"WSL_INTEROP", "WSL_DISTRO_NAME", "container"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("WSL_INTEROP", "")
			t.Setenv("WSL_DISTRO_NAME", "")
			t.Setenv("container", "")
			value := "test"
			if key == "container" {
				value = "systemd-nspawn"
			}
			t.Setenv(key, value)
			if !BasicPasswordStore() || CopyMineAvailable() {
				t.Fatal("forced environment must use basic and disable copy-mine")
			}
		})
	}
}

func TestProfileDiscovery(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Default", "Profile 2", "Empty"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if name != "Empty" {
			if err := os.WriteFile(filepath.Join(root, name, "Preferences"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(root, "Default"), filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(`{"profile":{"info_cache":{"Default":{"name":"Personal"},"Missing":{"name":"Old"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	want := []Profile{{"Default", "Personal"}, {"Profile 2", "Profile 2"}}
	if got := profiles(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %#v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := profiles(root); got[0].Name != "Default" {
		t.Fatalf("fallback = %#v", got)
	}
	if got := profiles(filepath.Join(root, "missing")); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestOwnerRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/test/config")
	if got := OwnerRoot(); got != "/test/config/chromium" {
		t.Fatal(got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/test/home")
	if got := OwnerRoot(); got != "/test/home/.config/chromium" {
		t.Fatal(got)
	}
}
