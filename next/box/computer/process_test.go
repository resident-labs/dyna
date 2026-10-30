package computer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCgroupLimitAndFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("1000"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("123\n456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := cgroup(root)
	if err != nil {
		t.Fatal(err)
	}
	group.Close()
	moved, err := os.ReadFile(filepath.Join(root, "agent/cgroup.procs"))
	if err != nil || string(moved) != "456" {
		t.Fatalf("second process not moved: %q, %v", moved, err)
	}
	value, err := os.ReadFile(filepath.Join(root, "computer/memory.max"))
	if err != nil || string(value) != "500" {
		t.Fatalf("memory limit %q: %v", value, err)
	}
	if _, err := cgroup(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing cgroup must fall back")
	}
}

func TestLaunchArgs(t *testing.T) {
	args := strings.Join(launchArgs, " ")
	for _, flag := range []string{"--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=/box/computer", "--disable-dev-shm-usage", "--no-first-run", "about:blank"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %s", flag)
		}
	}
}

func TestOOMExitReportedOnce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "computer")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "wait")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nif [ ! -f %q ]; then touch %q; echo 'oom_kill 1' > %q; exit 1; fi\nread x < %q\n", filepath.Join(root, "started"), filepath.Join(root, "started"), filepath.Join(dir, "memory.events"), fifo)
	if err := os.WriteFile(filepath.Join(root, "chromium"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	p := &Process{root: root}
	defer p.Close()
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-p.done
	report, err := p.Start(context.Background())
	if err != nil || !strings.Contains(report, "memory limit") {
		t.Fatalf("report %q: %v", report, err)
	}
	report, err = p.Start(context.Background())
	if err != nil || report != "" {
		t.Fatalf("second report %q: %v", report, err)
	}
}

func TestOOMCount(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "computer"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "computer/memory.events"), []byte("oom 3\noom_kill 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := oomCount(root); got != 2 {
		t.Fatalf("oom kills = %d", got)
	}
}
