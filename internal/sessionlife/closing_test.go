package sessionlife

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A close check whose holder died, such as 'dyna subagent finish' killed when
// its turn ended, or one far older than any check takes, must not wedge the
// session. A live, fresh check still blocks.
func TestCheckIgnoresAbandonedCloseReservation(t *testing.T) {
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	sessions := t.TempDir()
	write := func(closing map[string]any) string {
		id := "ses_20261018-165645-bffdeee5"
		if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"id": id, "closing": closing})
		if err := os.WriteFile(filepath.Join(sessions, id, "session.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := Check(sessions, write(map[string]any{"token": "t", "pid": dead.Process.Pid})); err != nil {
		t.Fatalf("dead holder still blocks: %v", err)
	}
	if err := Check(sessions, write(map[string]any{"token": "t", "pid": os.Getpid(), "at": time.Now().Add(-ClosingLimit - time.Minute)})); err != nil {
		t.Fatalf("expired reservation still blocks: %v", err)
	}
	if err := Check(sessions, write(map[string]any{"token": "t", "pid": os.Getpid(), "at": time.Now()})); err == nil {
		t.Fatal("live reservation did not block")
	}
}
