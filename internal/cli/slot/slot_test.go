package slot

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSixRunsUseAtMostTwoSlots(t *testing.T) {
	dir := t.TempDir()
	retry := make(chan struct{}, 6)
	waiting := make(chan struct{}, 6)
	started := make(chan struct{}, 6)
	finish := make(chan struct{}, 6)
	errs := make(chan error, 6)
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var notice bytes.Buffer
			lock, err := acquire(context.Background(), dir, 2, "test", &notice, func(context.Context) error {
				waiting <- struct{}{}
				<-retry
				return nil
			})
			if err != nil {
				errs <- err
				return
			}
			current := active.Add(1)
			for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
			}
			started <- struct{}{}
			<-finish
			active.Add(-1)
			lock.Close()
			errs <- nil
		}()
	}
	<-started
	<-started
	for i := 0; i < 4; i++ {
		<-waiting
	}
	for i := 0; i < 4; i++ {
		finish <- struct{}{}
		// A finished worker confirms its lock closed before another retries.
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		retry <- struct{}{}
		<-started
	}
	finish <- struct{}{}
	finish <- struct{}{}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrent runs = %d, want 2", got)
	}
}

func TestWaitingNoticeAndRelease(t *testing.T) {
	dir := t.TempDir()
	first, err := acquire(context.Background(), dir, 1, "session-one", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	second, err := acquire(context.Background(), dir, 1, "session-two", &notice, func(context.Context) error {
		return first.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	want := "waiting for a test slot: 1 of 1 in use, held by session-one\n"
	if notice.String() != want {
		t.Fatalf("notice = %q, want %q", notice.String(), want)
	}
}

func TestWaitTimeoutUsesOneNotice(t *testing.T) {
	dir := t.TempDir()
	first, err := acquire(context.Background(), dir, 1, "holder", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var notice bytes.Buffer
	attempts := 0
	_, err = acquire(context.Background(), dir, 1, "waiter", &notice, func(context.Context) error {
		attempts++
		if attempts == 2 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "30 minutes") {
		t.Fatalf("timeout: %v", err)
	}
	if got := strings.Count(notice.String(), "\n"); got != 1 {
		t.Fatalf("notice lines = %d", got)
	}
}

func TestNestedRunPreservesEnvironmentAndExitCode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv(heldEnv, "1")
	t.Setenv("XDG_RUNTIME_DIR", "")
	var output bytes.Buffer
	code, err := runCommand([]string{"/bin/sh", "-c", `printf '%s %s' "$HOME" "$DYNA_TEST_SLOT_HELD"; exit 7`}, []string{"HOME=/private", heldEnv + "=0"}, nil, &output, io.Discard)
	if err != nil || code != 7 || output.String() != "/private 1" {
		t.Fatalf("code=%d output=%q error=%v", code, output.String(), err)
	}
}

func TestKilledHolderReleasesLock(t *testing.T) {
	if dir := os.Getenv("DYNA_SLOT_HELPER_DIR"); dir != "" {
		lock, err := acquire(context.Background(), dir, 1, "helper", io.Discard, nil)
		if err != nil {
			os.Exit(2)
		}
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		lock.Close()
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestKilledHolderReleasesLock$")
	child.Env = append(os.Environ(), "DYNA_SLOT_HELPER_DIR="+dir)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper readiness %q: %v", line, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	lock, err := acquire(context.Background(), dir, 1, "next", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}
