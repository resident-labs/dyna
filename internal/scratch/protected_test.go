package scratch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweepSkipsProtectedPathsAndLogsOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	for _, mode := range []os.FileMode{0500, 0000} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			isolateRoot(t)
			blocked, err := Ensure("a-protected")
			if err != nil {
				t.Fatal(err)
			}
			note := filepath.Join(blocked, "keep")
			if err := os.WriteFile(note, []byte("evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			other, err := Ensure("z-expired")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(blocked, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
			var logs []string
			opts := Options{Pressure: true, Lookup: func(string) Owner { return Owner{Known: true, ClosedAt: time.Now().Add(-48 * time.Hour)} }, Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
			for pass := 0; pass < 2; pass++ {
				result, err := Sweep(t.Context(), opts)
				if err != nil {
					t.Fatalf("pass %d: protected scratch stopped cleanup: %v", pass, err)
				}
				if len(result.Kept) != 1 || result.Kept[0].Path != blocked {
					t.Fatalf("protected entry missing: %+v", result)
				}
				if len(logs) != 1 || !strings.Contains(logs[0], blocked) {
					t.Fatalf("pass %d logs: %v", pass, logs)
				}
				if _, err := os.Stat(other); !os.IsNotExist(err) {
					t.Fatalf("cleanup did not continue to next entry: %v", err)
				}
			}
			if err := os.Chmod(blocked, 0700); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(note)
			if err != nil || string(data) != "evidence" {
				t.Fatalf("protected evidence changed: %q: %v", data, err)
			}
		})
	}
}
