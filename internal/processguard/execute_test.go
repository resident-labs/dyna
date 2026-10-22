package processguard

import (
	"context"
	"errors"
	"os"
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
