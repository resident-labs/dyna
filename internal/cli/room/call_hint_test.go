package room

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/testsafe"
)

func TestPostCallHintDoesNotParseRecipientAddress(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	clearStamp(t)
	work := workRoom(t)
	saved := sendPost
	t.Cleanup(func() { sendPost = saved })
	sendPost = func(context.Context, notify.PostRequest) (notify.Result, error) {
		return notify.Result{
			Message:    room.Message{ID: "msg_0123456789abcdef", RoomID: work.ID, To: []string{work.Name + ":" + room.VoiceAgent}},
			Deliveries: []notify.Delivery{{To: work.Name, Delivery: "room"}},
		}, nil
	}
	cmd := PostCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	if _, err := execCmd(t, cmd, "@work", "CI needs a sign-in"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "No call is live") {
		t.Fatal(stderr.String())
	}
}

func TestVoicePostPrintsExplicitCallHint(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
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
