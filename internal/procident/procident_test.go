package procident

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// A test tree answers only the processes it holds.
func TestReadUsesTheGivenTree(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(root+"/7", 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "7 (cmd with ) space) S 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 4242 0 0\n"
	if err := os.WriteFile(root+"/7/stat", []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, 7)
	if err != nil || got != (Stat{PID: 7, ParentPID: 1, StartTime: 4242}) {
		t.Fatalf("Read = %+v, %v; want pid 7, parent 1, start 4242", got, err)
	}
	if _, err := Read(root, 8); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read of a missing process = %v; want a missing file", err)
	}
}
