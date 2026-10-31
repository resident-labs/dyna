package docs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// receiver is the forced command on the second machine, the same file the owner installs.
const receiver = "backup-recv.sh"

// copyLimit is the largest copy the receiver takes, 16 GiB.
const copyLimit = 16 << 30

// floorKiB is the free space the receiver keeps for other work, 50 GiB in KiB.
const floorKiB = 50 << 20

// roomyKiB is free space well above the floor, so only the other checks decide.
const roomyKiB = 1 << 40

// install copies the receiver into a new folder with a fake df that reports freeKiB free space.
// The receiver writes its copies into its own folder, so the returned path also names that folder.
func install(t *testing.T, freeKiB int64) string {
	t.Helper()
	src, err := os.ReadFile(receiver)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, receiver)
	if err := os.WriteFile(script, src, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	df := fmt.Sprintf("#!/bin/sh\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\nfake 1 1 %d 1%% /\\n'\n", freeKiB)
	if err := os.WriteFile(filepath.Join(bin, "df"), []byte(df), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

// receive runs the receiver the way sshd does: the request is SSH_ORIGINAL_COMMAND and body is stdin.
// The fake df in the receiver folder comes first on PATH. It returns the exit status.
func receive(t *testing.T, script, request string, body []byte) int {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"SSH_ORIGINAL_COMMAND="+request,
		"PATH="+filepath.Join(filepath.Dir(script), "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Stdin = bytes.NewReader(body)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return exit.ExitCode()
}

// copies lists the finished copies in dir, and any partial file that a refused copy left.
func copies(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "dyna-*.tar.gz.age"))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := filepath.Glob(filepath.Join(dir, ".dyna-*.partial"))
	if err != nil {
		t.Fatal(err)
	}
	return append(names, partial...)
}

func TestBackupReceiver(t *testing.T) {
	body := []byte("encrypted copy bytes")

	t.Run("a full copy is kept", func(t *testing.T) {
		script := install(t, roomyKiB)
		if code := receive(t, script, strconv.Itoa(len(body)), body); code != 0 {
			t.Fatalf("exit status %d; want 0", code)
		}
		names := copies(t, filepath.Dir(script))
		if len(names) != 1 {
			t.Fatalf("copies: %v", names)
		}
		got, err := os.ReadFile(names[0])
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("kept copy = %q, %v; want the bytes sent", got, err)
		}
	})

	t.Run("a short copy is refused and leaves nothing", func(t *testing.T) {
		script := install(t, roomyKiB)
		if code := receive(t, script, strconv.Itoa(len(body)+1), body); code != 65 {
			t.Fatalf("exit status %d; want 65", code)
		}
		if left := copies(t, filepath.Dir(script)); len(left) != 0 {
			t.Fatalf("files after a short copy = %v; want none", left)
		}
	})

	t.Run("a name supplied by the sender is refused", func(t *testing.T) {
		script := install(t, roomyKiB)
		if code := receive(t, script, "dyna-future.tar.gz.age 1", body); code != 64 {
			t.Fatalf("exit status %d", code)
		}
	})

	t.Run("a copy over 16 GiB is refused before any bytes are read", func(t *testing.T) {
		script := install(t, roomyKiB)
		if code := receive(t, script, strconv.Itoa(copyLimit+1), body); code != 64 {
			t.Fatalf("exit status %d; want 64", code)
		}
		if left := copies(t, filepath.Dir(script)); len(left) != 0 {
			t.Fatalf("files after an oversize copy = %v; want none", left)
		}
	})

	t.Run("a copy with less than 50 GiB left is refused with 66", func(t *testing.T) {
		script := install(t, floorKiB-1)
		if code := receive(t, script, strconv.Itoa(len(body)), body); code != 66 {
			t.Fatalf("exit status %d; want 66", code)
		}
		if left := copies(t, filepath.Dir(script)); len(left) != 0 {
			t.Fatalf("files after a full disk = %v; want none", left)
		}
	})

	t.Run("the newest 14 copies are kept", func(t *testing.T) {
		script := install(t, roomyKiB)
		dir := filepath.Dir(script)
		for day := 1; day <= 14; day++ {
			old := filepath.Join(dir, fmt.Sprintf("dyna-202610%02dT000000Z.tar.gz.age", day))
			if err := os.WriteFile(old, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if code := receive(t, script, strconv.Itoa(len(body)), body); code != 0 {
			t.Fatalf("exit status %d; want 0", code)
		}
		if kept := copies(t, dir); len(kept) != 15 {
			t.Fatalf("recent copies kept = %d; want 15", len(kept))
		}
		oldest := filepath.Join(dir, "dyna-20261001T000000Z.tar.gz.age")
		at := time.Now().Add(-16 * 24 * time.Hour)
		if err := os.Chtimes(oldest, at, at); err != nil {
			t.Fatal(err)
		}
		if code := receive(t, script, strconv.Itoa(len(body)), body); code != 0 {
			t.Fatalf("exit status %d; want 0", code)
		}
		kept := copies(t, dir)
		if len(kept) != 15 {
			t.Fatalf("copies kept = %d; want 15: %v", len(kept), kept)
		}
		if _, err := os.Stat(filepath.Join(dir, "dyna-20261001T000000Z.tar.gz.age")); !os.IsNotExist(err) {
			t.Fatalf("oldest copy still there: %v", err)
		}
	})
}
