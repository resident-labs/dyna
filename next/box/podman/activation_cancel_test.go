package podman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A native SSH stand-in whose remote shell survives the death of the local SSH process.
type remoteDetachedSSH struct{}

func (remoteDetachedSSH) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	return (ExecRunner{}).Run(ctx, in, "bash", "-c", `setsid bash -c "$1" <&0 & wait`, "remote-ssh", args[len(args)-1])
}

func TestCancelledOldActivationCannotDowngrade(t *testing.T) {
	tools := t.TempDir()
	entered, release := filepath.Join(tools, "entered"), filepath.Join(tools, "release")
	realHash := "/usr/bin/sha256sum"
	script := `#!/bin/bash
input=$(cat)
if [[ $input == old-engine ]]; then
 touch "$REMOTE_ENTERED"
 while [[ ! -f $REMOTE_RELEASE ]]; do sleep 0.01; done
fi
printf '%s' "$input" | ` + realHash + ` 
`
	if err := os.WriteFile(filepath.Join(tools, "sha256sum"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE_ENTERED", entered)
	t.Setenv("REMOTE_RELEASE", release)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	bin := t.TempDir()
	sum := func(raw string) string {
		h := sha256.Sum256([]byte(raw))
		s := hex.EncodeToString(h[:])
		if err := os.Mkdir(filepath.Join(bin, s), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, s, "dyna"), []byte(raw), 0755); err != nil {
			t.Fatal(err)
		}
		return s
	}
	oldSum, newSum := sum("old-engine"), sum("new-engine")
	h, err := New("user@host", t.TempDir(), "host", remoteDetachedSSH{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.ActivateBinary(ctx, bin, oldSum) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old checksum never entered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("old activation was not cancelled")
	}
	if err := h.ActivateBinary(context.Background(), bin, newSum); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	var target string
	for time.Now().Before(deadline) {
		target, err = os.Readlink(filepath.Join(bin, "current"))
		if err == nil && target == oldSum {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if strings.TrimSpace(target) != newSum {
		t.Fatalf("late old activation downgraded current: got %s, want new %s", target, newSum)
	}
}

func TestBrokenZstdFallsBackToGzip(t *testing.T) {
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "zstd"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then exit 0; fi\necho decompression-not-supported >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	h, err := New("user@host", t.TempDir(), "host", &localBinaryRunner{})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("engine")
	hash := sha256.Sum256(raw)
	if err := h.StageBinary(context.Background(), t.TempDir(), hex.EncodeToString(hash[:]), func(codec BinaryEncoding) ([]byte, error) { return CompressBinary(raw, codec) }); err != nil {
		t.Fatalf("usable gzip ignored: %v", err)
	}
}
