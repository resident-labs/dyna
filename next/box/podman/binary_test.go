package podman

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

type binaryRunner struct {
	args   []string
	input  []byte
	output string
}

func (r *binaryRunner) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	r.args = args
	r.input = input
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", r.output, len(r.output))), nil
}

func TestInstallBinarySendsFramedBytes(t *testing.T) {
	r := &binaryRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.InstallBinary(context.Background(), "/data", "v1", []byte("binary")); err != nil {
		t.Fatal(err)
	}
	command := r.args[len(r.args)-1]
	if !strings.HasSuffix(command, "'dyna-install' '/data' 'v1'") || !strings.Contains(command, "sha256sum") || !strings.Contains(command, "mv -Tf") {
		t.Fatalf("command %q", command)
	}
	if string(r.input) != "DYNA-IN 00000000000000000006\nbinary" {
		t.Fatalf("input %q", r.input)
	}
	if err := h.InstallBinary(context.Background(), "/data", "../escape", nil); err == nil {
		t.Fatal("accepted invalid version")
	}
}

type concurrentInstallRunner struct {
	commands chan string
	release  chan struct{}
}

func (r *concurrentInstallRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	r.commands <- args[len(args)-1]
	<-r.release
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestConcurrentInstallsUsePrivateTemporaryLinks(t *testing.T) {
	r := &concurrentInstallRunner{commands: make(chan string, 2), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, version := range []string{"v1", "v2"} {
		go func() { done <- h.InstallBinary(context.Background(), "/data", version, []byte("binary")) }()
	}
	for range 2 {
		command := <-r.commands
		if !strings.Contains(command, "mktemp -d .current.XXXXXX") || !strings.Contains(command, `"$linkdir/current" current`) || !strings.Contains(command, `rm -rf -- "$linkdir"`) {
			t.Errorf("unsafe temporary link: %s", command)
		}
	}
	close(r.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostArchitectureCheck(t *testing.T) {
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	r := &binaryRunner{output: machine + "\n"}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if machine != "" {
		if err := h.checkArch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasSuffix(r.args[len(r.args)-1], "'uname' '-m'") {
		t.Fatalf("argv %v", r.args)
	}
	r.output = "unsupported\n"
	if err := h.checkArch(context.Background()); err == nil {
		t.Fatal("accepted different architecture")
	}
}
