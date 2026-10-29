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

func TestMetadataEnvDropsEngineSecrets(t *testing.T) {
	secrets := []string{"GH_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_HOST_CREDS_FILE", "XAI_API_KEY"}
	for _, key := range secrets {
		t.Setenv(key, "must-not-reach-children")
	}
	t.Setenv("GH_CONFIG_DIR", "configured")
	t.Setenv("ANTHROPIC_BASE_URL", "configured")

	kept := 0
	for _, entry := range MetadataEnv() {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains(secrets, key) {
			t.Errorf("child environment contains %s", key)
		}
		if entry == "GH_CONFIG_DIR=configured" || entry == "ANTHROPIC_BASE_URL=configured" {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("child environment lost configuration, kept %d of 2", kept)
	}
}

func TestWithoutSecretsKeepsConfiguration(t *testing.T) {
	env := []string{"GH_TOKEN=s", "NPM_TOKEN=s", "OPENAI_API_KEY=s", "OPENAI_BASE_URL=u", "GH_CONFIG_DIR=c", "HOME=h"}
	got := WithoutSecrets(env)
	want := []string{"OPENAI_BASE_URL=u", "GH_CONFIG_DIR=c", "HOME=h"}
	if !slices.Equal(got, want) {
		t.Fatalf("WithoutSecrets = %q, want %q", got, want)
	}
	if len(env) != 6 {
		t.Fatal("WithoutSecrets changed its input")
	}
}

func TestWithoutEngineSecretsKeepsSessionGitHubToken(t *testing.T) {
	env := []string{"GH_TOKEN=s", "GH_ENTERPRISE_TOKEN=e", "NPM_TOKEN=s", "OPENAI_API_KEY=s", "HOME=h"}
	got := WithoutEngineSecrets(env)
	want := []string{"GH_TOKEN=s", "GH_ENTERPRISE_TOKEN=e", "HOME=h"}
	if !slices.Equal(got, want) {
		t.Fatalf("WithoutEngineSecrets = %q, want %q", got, want)
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
