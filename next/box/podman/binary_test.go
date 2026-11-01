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

func TestInstallBinarySkipsBytesWhenHostHasSum(t *testing.T) {
	read := func() ([]byte, error) { return []byte("binary"), nil }
	for _, tc := range []struct {
		name, hostSum string
		sentBytes     bool
	}{
		{name: "same sum", hostSum: "abc\n"},
		{name: "other sum", hostSum: "def\n", sentBytes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &binaryRunner{output: tc.hostSum}
			h, err := New("user@host", t.TempDir(), "host", r)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.InstallBinary(context.Background(), "/data/bin", "v1", "abc", read); err != nil {
				t.Fatal(err)
			}
			command := r.args[len(r.args)-1]
			if tc.sentBytes {
				if !strings.Contains(command, "'dyna-install' '/data/bin' 'v1'") || string(r.input) != "DYNA-IN 00000000000000000006\nbinary" {
					t.Fatalf("command %q input %q", command, r.input)
				}
				return
			}
			if !strings.Contains(command, "'dyna-link' '/data/bin' 'v1'") || string(r.input) != "DYNA-IN 00000000000000000000\n" {
				t.Fatalf("command %q input %q", command, r.input)
			}
		})
	}
	h, err := New("user@host", t.TempDir(), "host", &binaryRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.InstallBinary(context.Background(), "/data/bin", "../escape", "abc", read); err == nil {
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
	command := args[len(args)-1]
	if !strings.Contains(command, "'dyna-install'") {
		return []byte("DYNA-END 00000000000000000000 000\n"), nil
	}
	r.commands <- command
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
	read := func() ([]byte, error) { return []byte("binary"), nil }
	for _, version := range []string{"v1", "v2"} {
		go func() { done <- h.InstallBinary(context.Background(), "/data/bin", version, "abc", read) }()
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
