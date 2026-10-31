package podman

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type instructionRunner struct{ runner }

func (r *instructionRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return r.runner.Run(ctx, input, args...)
		}
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Output()
}

func TestSyncInstructionRepairsRemoteContent(t *testing.T) {
	r := &instructionRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rooms", "room", "AGENTS.md")
	for _, value := range []string{"first", "second"} {
		if err := h.SyncInstructions(context.Background(), []InstructionFile{{Path: path, Data: []byte(value)}}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != value {
			t.Fatalf("got %q: %v", got, err)
		}
	}
}
