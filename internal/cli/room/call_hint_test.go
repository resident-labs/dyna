package room

import (
	"bytes"
	"strings"
	"testing"
)

func TestVoicePostPrintsExplicitCallHint(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	clearStamp(t)
	rec := workRoom(t)
	cmd := PostCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	if _, err := execCmd(t, cmd, "@"+rec.Name+":voice-call", "CI needs a sign-in"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "No call is live; use `dyna call --reason` to ring the owner.") {
		t.Fatal(stderr.String())
	}
}
