package processguard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExecuteBoundsProcessDescendants(t *testing.T) {
	var output strings.Builder
	start := time.Now()
	err := Execute(context.Background(), Process{Args: []string{"/bin/sh", "-c", "sleep 30 & wait"}, Env: MetadataEnv(), Timeout: 50 * time.Millisecond, Output: &output})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout did not bound descendants: %v", err)
	}
}

func TestExecuteNamesAMissingWorkingDirectory(t *testing.T) {
	err := Execute(context.Background(), Process{
		Dir:     filepath.Join(t.TempDir(), "gone"),
		Args:    []string{"/usr/bin/git", "rev-parse", "--show-toplevel"},
		Env:     MetadataEnv(),
		Timeout: 10 * time.Second,
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing directory must stay ErrNotExist: %v", err)
	}
	// The kernel reports a failed chdir as a failed exec of the binary, which
	// reads as Git being gone. The error must name the directory instead.
	if !strings.Contains(err.Error(), "gone") || strings.Contains(err.Error(), "fork/exec") {
		t.Fatalf("error must name the missing working directory, got: %v", err)
	}
}

func TestMetadataEnvDropsAllDynaVariables(t *testing.T) {
	for _, key := range []string{"DYNA_BIN", "DYNA_SCRATCH", "DYNA_HOME", "DYNA_FUTURE_VARIABLE"} {
		t.Setenv(key, "must-not-reach-children")
	}
	t.Setenv("PROCESS_ENV_KEEP", "preserved")

	kept := false
	for _, entry := range MetadataEnv() {
		if strings.HasPrefix(entry, "DYNA_") {
			t.Errorf("child environment contains %q", entry)
		}
		kept = kept || entry == "PROCESS_ENV_KEEP=preserved"
	}
	if !kept {
		t.Error("child environment lost unrelated variable")
	}

}

func TestMetadataEnvDropsGitHubTokens(t *testing.T) {
	tokens := []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	for _, key := range tokens {
		t.Setenv(key, "must-not-reach-children")
	}
	t.Setenv("GH_CONFIG_DIR", "configured")

	kept := false
	for _, entry := range MetadataEnv() {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains(tokens, key) {
			t.Errorf("child environment contains %s", key)
		}
		kept = kept || entry == "GH_CONFIG_DIR=configured"
	}
	if !kept {
		t.Error("child environment lost GH_CONFIG_DIR")
	}
}

func TestMetadataEnvGoFlags(t *testing.T) {
	for _, tc := range []struct{ name, flags, want string }{
		{"unset", "", "-trimpath"},
		{"empty", "", "-trimpath"},
		{"preserved", "-tags=unit -mod=readonly", "-tags=unit -mod=readonly -trimpath"},
		{"enabled", "-trimpath", "-trimpath"},
		{"disabled", "-trimpath=false", "-trimpath=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFLAGS", tc.flags)
			if tc.name == "unset" {
				if err := os.Unsetenv("GOFLAGS"); err != nil {
					t.Fatal(err)
				}
			}

			want := "GOFLAGS=" + tc.want
			var got []string
			for _, entry := range MetadataEnv() {
				if strings.HasPrefix(entry, "GOFLAGS=") {
					got = append(got, entry)
				}
			}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("child environment GOFLAGS = %q, want %q", got, want)
			}

		})
	}
}
