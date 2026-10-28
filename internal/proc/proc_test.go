package proc

import (
	"os"
	"path/filepath"
	"testing"
)

// The proc boundary returns typed identity, CPU and memory values, including
// when the kernel's parenthesized command contains a closing parenthesis.
func TestReadParsesProcessInventory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "7")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "7 (cmd with ) space) S 1 7 7 0 -1 0 0 0 0 0 11 12 0 0 0 0 0 0 4242 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "statm"), []byte("100 25 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, 7)
	want := Process{
		PID: 7, ParentPID: 1, Command: "cmd with ) space", State: "S",
		StartTime: 4242, CPUTicks: 23, Memory: 25 * uint64(os.Getpagesize()),
	}
	if err != nil || got != want {
		t.Fatalf("Read = %+v, %v; want %+v", got, err, want)
	}
}
