package api

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSocketGeneration(t *testing.T) {
	for _, scenario := range []string{"absent", "registered"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			sockets := NewSockets(New(&fakeService{}, nil), root, uint32(os.Getuid()), nil)
			defer sockets.Close()
			if scenario == "registered" {
				if err := sockets.AddBox("b", "s", "r"); err != nil {
					t.Fatal(err)
				}
			}
			old := sockets.BoxGeneration("b")
			if err := sockets.RemoveBox("b"); err != nil {
				t.Fatal(err)
			}
			if err := sockets.AddBox("b", "s", "r"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "boxes", "b.sock")
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := sockets.RemoveBoxAtGeneration("b", old); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("old generation retired a replacement: %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("replacement socket was changed")
			}
			connection, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			connection.Close()
			// Positive control: exactly the current generation still revokes its socket.
			current := sockets.BoxGeneration("b")
			if err := sockets.RemoveBoxAtGeneration("b", current); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("current generation did not remove socket: %v", err)
			}
		})
	}
}
