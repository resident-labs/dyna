//go:build linux

package computers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserEndpointIsLocalAndValidated(t *testing.T) {
	browserTestEnvironment(t)
	id := "endpoint"
	if err := os.MkdirAll(computerBrowserProfile(id), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ contents, want string }{
		{"45678\n/devtools/browser/private-id\n", "ws://127.0.0.1:45678/devtools/browser/private-id"},
		{"0\n/devtools/browser/id\n", ""},
		{"65536\n/devtools/browser/id\n", ""},
		{"45678\n//other-host/path\n", ""},
		{"45678\n/devtools/browser/id?host=other\n", ""},
		{"45678\n/devtools/browser/id\nextra\n", ""},
	} {
		if err := os.WriteFile(filepath.Join(computerBrowserProfile(id), "DevToolsActivePort"), []byte(tc.contents), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := browserEndpoint(id)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted %q: %s", tc.contents, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("endpoint = %q, %v; want %q", got, err, tc.want)
		}
	}
}
