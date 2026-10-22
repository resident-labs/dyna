package childenv

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCleanProtectsInheritedDescriptors(t *testing.T) {
	for _, key := range []string{"DYNA_SUBAGENT_LOCK_FD"} {
		t.Run(key, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "lock")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			// Dup reproduces ExtraFiles adoption: the descriptor starts without CLOEXEC.
			fd, err := syscall.Dup(int(file.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Close(fd)
			t.Setenv("DYNA_SUBAGENT_LOCK_FD", "invalid")
			t.Setenv(key, strconv.Itoa(fd))
			cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("readlink /proc/self/fd/%d || true", fd))
			// A filtered environment must still protect the inherited descriptor.
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			Clean(cmd)
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				t.Fatalf("lock descriptor was closed: %v", err)
			}
			if flags&unix.FD_CLOEXEC == 0 {
				t.Fatal("inherited descriptor lacks CLOEXEC")
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(out)) == file.Name() {
				t.Fatal("child inherited the lock descriptor")
			}
		})
	}
}

func TestCleanAllowsIntentionalExtraFilesTransfer(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", strconv.Itoa(int(file.Fd())))
	cmd := exec.Command("/bin/sh", "-c", "readlink /proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	out, err := Clean(cmd).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != file.Name() {
		t.Fatalf("intentional fd 3 transfer = %q, want %q", out, file.Name())
	}
}
