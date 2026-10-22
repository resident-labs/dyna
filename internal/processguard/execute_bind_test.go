package processguard

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExecuteFailedBindRunsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "native")
	failure := errors.New("record refused")
	err := Execute(t.Context(), Process{
		Args: []string{"/bin/sh", "-c", "printf ran > \"$1\"", "sh", marker},
		Env:  MetadataEnv(), Timeout: time.Minute,
		Bind: func(ctx context.Context, ref Reference) error {
			pid, birth := ref.Leader()
			if pid <= 1 || birth == "" {
				t.Errorf("bind has no live anchor: %d, %q", pid, birth)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("native ran before bind: %v", err)
			}
			return failure
		},
	})
	if !errors.Is(err, failure) {
		t.Fatalf("bind error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("failed bind started native: %v", err)
	}
}

func TestExecuteBoundCommandCancelsOnEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- Execute(ctx, Process{
			Args: []string{"/bin/sh", "-c", "printf ready; sleep 30 & wait"},
			Env:  MetadataEnv(), Timeout: time.Minute, Output: writer,
			Bind: func(context.Context, Reference) error { return nil },
		})
		writer.Close()
	}()
	var ready [5]byte
	if _, err := io.ReadFull(reader, ready[:]); err != nil || string(ready[:]) != "ready" {
		t.Fatalf("native did not start: %q, %v", ready, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("bound cancellation = %v", err)
	}
}

func TestExecuteBindReceivesBoundedContext(t *testing.T) {
	_, hadDeadline := t.Context().Deadline()
	ctxHadDeadline := true
	err := Execute(t.Context(), Process{
		Args: []string{"/bin/true"}, Env: MetadataEnv(), Timeout: time.Minute,
		Bind: func(ctx context.Context, _ Reference) error {
			_, ctxHadDeadline = ctx.Deadline()
			return context.Canceled
		},
	})
	if !ctxHadDeadline || !errors.Is(err, context.Canceled) {
		t.Fatalf("bind lost its timeout: original deadline=%v, bounded=%v, error=%v", hadDeadline, ctxHadDeadline, err)
	}
}

func TestExecuteBoundCommandDropsLockDescriptor(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "inherited-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd := int(file.Fd())
	flags, err := unix.FcntlInt(file.Fd(), syscall.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(file.Fd(), syscall.F_SETFD, flags&^syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", strconv.Itoa(fd))
	var output strings.Builder
	err = Execute(t.Context(), Process{
		Args: []string{"/bin/sh", "-c", "readlink /proc/self/fd/" + strconv.Itoa(fd) + " 2>/dev/null || :"},
		Env:  MetadataEnv(), Timeout: time.Minute, Output: &output,
		Bind: func(context.Context, Reference) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) == file.Name() {
		t.Fatal("native command inherited the lock descriptor")
	}
}
