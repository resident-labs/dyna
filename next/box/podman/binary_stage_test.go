package podman

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Execute the actual remote shell, not a fake that always accepts its checksum.
type localBinaryRunner struct {
	sent      int
	forceGzip bool
}

func (r *localBinaryRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	r.sent += len(in)
	if r.forceGzip && strings.Contains(args[len(args)-1], "command -v zstd") {
		return []byte("gzip\nDYNA-END 00000000000000000005 000\n"), nil
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, out)
	}
	return out, nil
}
func packedBinary(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInstallRejectsCorruptBytesBeforePublishing(t *testing.T) {
	r := &localBinaryRunner{forceGzip: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	err = h.InstallBinary(context.Background(), bin, "v1", strings.Repeat("a", 64), func(BinaryEncoding) ([]byte, error) { return []byte("corrupt"), nil })
	if err == nil {
		t.Fatal("corrupt payload was published without matching requested SHA")
	}
	if _, err := os.Stat(filepath.Join(bin, "current")); !os.IsNotExist(err) {
		t.Fatalf("current published: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(bin, "*", ".dyna.*"))
	if len(files) > 0 {
		t.Fatalf("partial files: %v", files)
	}
}

func TestConcurrentSameSHAStagesPublishOnlyCompleteObjects(t *testing.T) {
	raw := bytes.Repeat([]byte("concurrent engine"), 1000)
	hash := sha256.Sum256(raw)
	sum := hex.EncodeToString(hash[:])
	packed := packedBinary(t, raw)
	bin := t.TempDir()
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		h, err := New("user@host", t.TempDir(), "host", &localBinaryRunner{forceGzip: true})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			done <- h.StageBinary(context.Background(), bin, sum, func(BinaryEncoding) ([]byte, error) { ready.Done(); <-release; return packed, nil })
		}()
	}
	ready.Wait()
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(bin, sum, "dyna"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("published object: %v", err)
	}
	partial, _ := filepath.Glob(filepath.Join(bin, sum, ".dyna.*"))
	if len(partial) > 0 {
		t.Fatalf("partial files: %v", partial)
	}
	if _, err := os.Lstat(filepath.Join(bin, "current")); !os.IsNotExist(err) {
		t.Fatalf("concurrent stage activated current: %v", err)
	}
}

func TestLateOldStagerCannotDowngradeNewActivation(t *testing.T) {
	bin := t.TempDir()
	oldRaw := []byte("old engine")
	newRaw := []byte("new engine")
	oldHash := sha256.Sum256(oldRaw)
	newHash := sha256.Sum256(newRaw)
	oldSum := hex.EncodeToString(oldHash[:])
	newSum := hex.EncodeToString(newHash[:])
	old, err := New("user@host", t.TempDir(), "old", &localBinaryRunner{forceGzip: true})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := New("user@host", t.TempDir(), "new", &localBinaryRunner{forceGzip: true})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	oldPacked := packedBinary(t, oldRaw)
	go func() {
		done <- old.StageBinary(context.Background(), bin, oldSum, func(BinaryEncoding) ([]byte, error) { close(entered); <-release; return oldPacked, nil })
	}()
	<-entered
	if err := newer.StageBinary(context.Background(), bin, newSum, func(BinaryEncoding) ([]byte, error) { return packedBinary(t, newRaw), nil }); err != nil {
		t.Fatal(err)
	}
	if err := newer.ActivateBinary(context.Background(), bin, newSum); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(bin, "current"))
	if err != nil || current != newSum {
		t.Fatalf("late old stage downgraded current to %q: %v", current, err)
	}
	for sum, want := range map[string][]byte{oldSum: oldRaw, newSum: newRaw} {
		got, err := os.ReadFile(filepath.Join(bin, sum, "dyna"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("object %s: %v", sum, err)
		}
	}
}

func TestPartialOrWrongCompressedObjectDoesNotReplaceCurrent(t *testing.T) {
	for _, kind := range []string{"truncated gzip", "wrong checksum"} {
		t.Run(kind, func(t *testing.T) {
			h, err := New("user@host", t.TempDir(), "host", &localBinaryRunner{forceGzip: true})
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if err := os.Symlink("previous", filepath.Join(bin, "current")); err != nil {
				t.Fatal(err)
			}
			payload := packedBinary(t, []byte("wrong engine"))
			if kind == "truncated gzip" {
				payload = payload[:len(payload)/2]
			}
			sum := strings.Repeat("a", 64)
			if err := h.StageBinary(context.Background(), bin, sum, func(BinaryEncoding) ([]byte, error) { return payload, nil }); err == nil {
				t.Fatal("bad payload accepted")
			}
			if _, err := os.Stat(filepath.Join(bin, sum, "dyna")); !os.IsNotExist(err) {
				t.Fatalf("published corrupt object: %v", err)
			}
			link, err := os.Readlink(filepath.Join(bin, "current"))
			if err != nil || link != "previous" {
				t.Fatalf("current changed: %q %v", link, err)
			}
			partial, _ := filepath.Glob(filepath.Join(bin, sum, ".dyna.*"))
			if len(partial) > 0 {
				t.Fatalf("partial files: %v", partial)
			}
		})
	}
}

func TestCompressedStageDoesNotActivate(t *testing.T) {
	r := &localBinaryRunner{forceGzip: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("engine binary"), 1000)
	hash := sha256.Sum256(raw)
	sum := hex.EncodeToString(hash[:])
	bin := t.TempDir()
	read := func(BinaryEncoding) ([]byte, error) { return packedBinary(t, raw), nil }
	if err := h.StageBinary(context.Background(), bin, sum, read); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(bin, sum, "dyna"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("staged bytes %d: %v", len(got), err)
	}
	if _, err := os.Lstat(filepath.Join(bin, "current")); !os.IsNotExist(err) {
		t.Fatalf("stage activated: %v", err)
	}
	if err := h.StageBinary(context.Background(), bin, sum, func(BinaryEncoding) ([]byte, error) { t.Fatal("cache hit requested payload"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateBinary(context.Background(), bin, sum); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(bin, "current"))
	if err != nil || target != sum {
		t.Fatalf("link %q: %v", target, err)
	}
}
