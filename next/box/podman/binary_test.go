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
	inputs [][]byte
}

func (r *binaryRunner) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	r.args = args
	r.input = input
	r.inputs = append(r.inputs, input)
	output := r.output
	if strings.Contains(args[len(args)-1], "command -v zstd") {
		output = "gzip\n"
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", output, len(output))), nil
}

func TestInstallBinarySkipsBytesWhenHostHasSum(t *testing.T) {
	read := func(BinaryEncoding) ([]byte, error) { return []byte("binary"), nil }
	for _, tc := range []struct {
		name, hostSum string
		sentBytes     bool
	}{
		{name: "same sum", hostSum: strings.Repeat("a", 64) + "\n"},
		{name: "other sum", hostSum: "def\n", sentBytes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &binaryRunner{output: tc.hostSum}
			h, err := New("user@host", t.TempDir(), "host", r)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.InstallBinary(context.Background(), "/data/bin", "v1", strings.Repeat("a", 64), read); err != nil {
				t.Fatal(err)
			}
			command := r.args[len(r.args)-1]
			payloads := 0
			for _, in := range r.inputs {
				if len(in) > len("DYNA-IN ")+20+1 {
					payloads++
				}
			}
			if (payloads > 0) != tc.sentBytes {
				t.Fatalf("payloads %d, want send %v", payloads, tc.sentBytes)
			}
			if !strings.Contains(command, "'dyna-link' '/data/bin' '"+strings.Repeat("a", 64)+"'") {
				t.Fatalf("activation: %s", command)
			}

		})
	}
	h, err := New("user@host", t.TempDir(), "host", &binaryRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.InstallBinary(context.Background(), "/data/bin", "../escape", strings.Repeat("a", 64), read); err == nil {
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
		if strings.Contains(command, "command -v zstd") {
			return []byte("gzip\nDYNA-END 00000000000000000005 000\n"), nil
		}
		return []byte("DYNA-END 00000000000000000000 000\n"), nil
	}
	r.commands <- command
	<-r.release
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestConcurrentInstallsUsePrivateOwnedTemporaries(t *testing.T) {
	r := &concurrentInstallRunner{commands: make(chan string, 2), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	read := func(BinaryEncoding) ([]byte, error) { return []byte("binary"), nil }
	for _, version := range []string{"v1", "v2"} {
		go func() {
			done <- h.InstallBinary(context.Background(), "/data/bin", version, strings.Repeat("a", 64), read)
		}()
	}
	previous := ""
	for range 2 {
		command := <-r.commands
		if command == previous || !strings.Contains(command, `temporary="$dir/.dyna.${DYNA_START_DIR##*/}"`) || !strings.Contains(command, `rm -rf -- "$temporary"`) || strings.Contains(command, "current") {
			t.Errorf("unsafe private temporary: %s", command)
		}
		previous = command
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
