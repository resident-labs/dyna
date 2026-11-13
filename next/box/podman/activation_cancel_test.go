package podman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	entered, release := newScriptSignal(t), newScriptSignal(t)
	realHash := "/usr/bin/sha256sum"
	script := `#!/bin/bash
input=$(cat)
if [[ $input == old-engine ]]; then
 printf '%s %s\n' "$$" "$(/usr/bin/ps -o pgid= -p "$$")" > "$REMOTE_ENTERED"
 IFS= read -r _ < "$REMOTE_RELEASE"
fi
printf '%s' "$input" | ` + realHash + `
`
	if err := os.WriteFile(filepath.Join(tools, "sha256sum"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE_ENTERED", entered.path)
	t.Setenv("REMOTE_RELEASE", release.path)
	t.Cleanup(func() { release.write(t, "go") })
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
	h, err := New("user@host", t.TempDir(), "host", remoteDetachedSSH{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.ActivateBinary(ctx, bin, oldSum) }()
	_, group := entered.process(t)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("old activation was not cancelled")
	}
	if err := h.ActivateBinary(context.Background(), bin, newSum); err != nil {
		t.Fatal(err)
	}
	assertRemoteGroupExited(t, group)
	release.write(t, "go")
	target, err := os.Readlink(filepath.Join(bin, "current"))
	if err != nil {
		t.Fatal(err)
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
	h, err := New("user@host", t.TempDir(), "host", &localBinaryRunner{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("engine")
	hash := sha256.Sum256(raw)
	if err := h.StageBinary(context.Background(), t.TempDir(), hex.EncodeToString(hash[:]), func(codec BinaryEncoding) ([]byte, error) { return CompressBinary(raw, codec) }); err != nil {
		t.Fatalf("usable gzip ignored: %v", err)
	}
}
