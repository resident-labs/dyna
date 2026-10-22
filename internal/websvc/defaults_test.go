package websvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/websvc/devicestore"
)

func TestLoopbackPort(t *testing.T) {
	for _, tc := range []struct{ addr, port string }{
		{"127.0.0.1:7457", "7457"},
		{"[::1]:9000", "9000"},
	} {
		if got, err := LoopbackPort(tc.addr); err != nil || got != tc.port {
			t.Errorf("LoopbackPort(%q) = %q, %v; want %q", tc.addr, got, err, tc.port)
		}
	}
	for _, addr := range []string{"0.0.0.0:7457", "192.168.1.5:7457", ":7457", "localhost:7457", "100.64.0.1:7457", "127.0.0.1:0", "127.0.0.1:"} {
		want := addr + " is not a loopback address with a port; the web server binds 127.0.0.1 or ::1 only"
		if _, err := LoopbackPort(addr); err == nil || err.Error() != want {
			t.Errorf("LoopbackPort(%q) error = %v; want %q", addr, err, want)
		}
	}
	if _, err := LoopbackPort("127.0.0.1"); err == nil || !strings.Contains(err.Error(), "missing port in address") {
		t.Errorf("missing port error = %v", err)
	}
}

func TestDefaultStore(t *testing.T) {
	for _, xdg := range []bool{true, false} {
		name := "HOME fallback"
		if xdg {
			name = "XDG data"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", "")
			want := filepath.Join(home, ".local", "share", "dyna", "web")
			if xdg {
				data := t.TempDir()
				t.Setenv("XDG_DATA_HOME", data)
				want = filepath.Join(data, "dyna", "web")
			}
			s, err := devicestore.DefaultStore()
			if err != nil {
				t.Fatal(err)
			}
			if s.Dir() != want {
				t.Fatalf("directory = %q; want %q", s.Dir(), want)
			}
			info, err := os.Stat(want)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("directory permissions = %o", info.Mode().Perm())
			}
		})
	}
}

func TestLocalRoute(t *testing.T) {
	for _, app := range []string{"http://127.0.0.1:7457", "http://localhost:7457", "http://[::1]:7457"} {
		if !LocalRoute(Route{App: app}) {
			t.Errorf("%s is a local route", app)
		}
	}
	for _, app := range []string{"https://m-abc.residentlabs.dev", "https://desk.example.ts.net", "http://192.168.1.4:7457", "https://127.0.0.1.example.com"} {
		if LocalRoute(Route{App: app}) {
			t.Errorf("%s is not a local route", app)
		}
	}
}
