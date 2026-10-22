//go:build linux

package computers

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWayVNCControlSocketBelongsToItsComputer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // No GPU probe is needed to check the command.
	controls := make(map[string]bool)
	for _, computer := range []string{"first", "second"} {
		dir := filepath.Join(t.TempDir(), computer)
		config := t.TempDir()
		rfb := filepath.Join(dir, "rfb.sock")
		args, _, err := (wayVNCBackend{}).Command(ctx, StreamOptions{Socket: rfb, ConfigDir: config})
		if err != nil {
			t.Fatal(err)
		}
		control := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-S" {
				control = args[i+1]
			}
		}
		if control == "" {
			t.Fatal("wayvnc must use a computer-specific control socket")
		}
		if filepath.Dir(control) != dir || control == rfb || controls[control] {
			t.Fatalf("control socket %q must be unique beside %q", control, rfb)
		}
		if args[len(args)-1] != rfb {
			t.Fatalf("RFB socket changed: %v", args)
		}
		controls[control] = true
	}
}
