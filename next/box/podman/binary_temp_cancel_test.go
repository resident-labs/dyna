package podman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type temporaryBinaryRunner struct{ nativeShellRunner }

func (r temporaryBinaryRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	if strings.Contains(args[len(args)-1], "command -v zstd") {
		return []byte("gzip\nDYNA-END 00000000000000000005 000\n"), nil
	}
	return r.nativeShellRunner.Run(ctx, in, args...)
}
func TestCanceledBinaryOperationsRemovePrivateTemporaries(t *testing.T) {
	for _, phase := range []string{"decode", "link"} {
		t.Run(phase, func(t *testing.T) {
			bin := t.TempDir()
			tools := t.TempDir()
			entered := filepath.Join(tools, "entered")
			name, script, pattern := "gzip", fmt.Sprintf("#!/bin/bash\nprintf partial\n: > %q\nsleep 1000\n", entered), filepath.Join(bin, "*", ".dyna.*")
			if phase == "link" {
				name = "mv"
				pattern = filepath.Join(bin, ".current.*")
				script = fmt.Sprintf("#!/bin/bash\nif [ \"$1\" = -Tf ]; then : > %q; sleep 1000; else exec /usr/bin/mv \"$@\"; fi\n", entered)
			}
			if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
			hash := sha256.Sum256([]byte("binary"))
			sum := hex.EncodeToString(hash[:])
			if phase == "link" {
				if err := os.Mkdir(filepath.Join(bin, sum), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, sum, "dyna"), []byte("binary"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			h, err := New("owner@host", t.TempDir(), "h", temporaryBinaryRunner{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var result error
			go func() {
				defer close(done)
				if phase == "link" {
					result = h.ActivateBinary(ctx, bin, sum)
				} else {
					result = h.StageBinary(ctx, bin, sum, func(BinaryEncoding) ([]byte, error) { return []byte("ignored"), nil })
				}
			}()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(entered); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("binary operation did not reach its barrier")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("binary cancellation did not join")
			}
			var unconfirmed RemoteCancellationUnconfirmed
			if result == nil || errors.As(result, &unconfirmed) {
				t.Fatalf("expected confirmed cancellation: %v", result)
			}
			paths, err := filepath.Glob(pattern)
			if err != nil || len(paths) != 0 {
				t.Fatalf("canceled %s left private temporaries after confirmed termination: %v, %v", phase, paths, err)
			}
		})
	}
}
