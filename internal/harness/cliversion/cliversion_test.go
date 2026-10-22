package cliversion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
)

// Parse accepts the version formats the supported CLIs print, including a v
// prefix, prereleases and build metadata, and refuses partial tokens.
func TestParse(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{"2.0.0\n", "2.0.0"},
		{"v2.1.2 (build)\n", "2.1.2"},
		{"v1.1.0", "1.1.0"},
		{"codex-cli 0.42.0\n", "0.42.0"},
		{"muse 1.4.10-R6427.1\n", "1.4.10-R6427.1"},
		{"2.1.2+build.4 x", "2.1.2+build.4"},
		{"v2.1.2-rc.1+abc)", "2.1.2-rc.1+abc"},
		{"muse 3.4.5-beta.2\n", "3.4.5-beta.2"},
		{"no-version-here\n", ""},
		{"xyz2.1.2", ""},
		{"2.1.2.3", ""},
		{"1.2.3.4", ""},
		{"build 7", ""},
		{"1.2", ""},
	} {
		if got := Parse([]byte(tc.output)); got != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.output, got, tc.want)
		}
	}
}

func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli")
	if err := fsutil.WriteExecutable(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Run reports one sentinel per outcome. The ordinary cases get a generous
// failure-only timeout: a loaded runner can delay shell startup, and only
// the hang case tests the deadline itself.
func TestRunOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		errErr  error
		timeout time.Duration
	}{
		{"answer", "printf 'cli 1.2.3\\n'", "1.2.3", nil, 30 * time.Second},
		{"stderr-answer", "printf 'cli 1.2.3\\n' >&2", "1.2.3", nil, 30 * time.Second},
		{"exit", "exit 3", "", ErrFailed, 30 * time.Second},
		{"silent", "printf 'nothing here'", "", ErrNoVersion, 30 * time.Second},
		{"hang", "/bin/sleep 30", "", ErrCanceled, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := fakeCLI(t, tc.body)
			got, err := Run(context.Background(), path, []string{"--version"}, []string{"HOME=" + t.TempDir()}, tc.timeout)
			if got != tc.want || !errors.Is(err, tc.errErr) {
				t.Fatalf("Run = %q, %v; want %q, %v", got, err, tc.want, tc.errErr)
			}
		})
	}
}

// A canceled context cancels the run before the command executes.
func TestRunCanceledContext(t *testing.T) {
	path := fakeCLI(t, "printf '1.2.3\\n'")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, path, nil, nil, time.Minute); !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled run: %v", err)
	}
}
