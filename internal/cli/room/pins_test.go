package room

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	dynastore "dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// servePins serves the engine's Pin and Unpin calls on the same room service
// the engine runs. This stand-in pins as the owner; the engine's pinner rules
// are tested in internal/engine.
func servePins(t *testing.T) {
	t.Helper()
	rooms := roomsvc.New(nil)
	pinAsOwner := func(roomRef, msgID string) error { return rooms.Pin(roomRef, msgID, "") }
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "Pin", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: pinAsOwner},
		{Name: "Unpin", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.Unpin},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, dynastore.DataDir(), calls)
}

func TestPinUnpinPinsCommands(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	servePins(t)
	s := store()
	rec, err := s.Create(room.CreateInput{Name: "alpha", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "\nFirst line\nsecond line"})
	if err != nil {
		t.Fatal(err)
	}
	svc := notify.Default()
	res, err := svc.Post(t.Context(), notify.PostInput{Sender: notify.Sender{Kind: notify.SenderOwner, Room: "alpha"}, Mentions: []string{"@root"}, Text: "a card",
		Files: []notify.File{{Name: "c.html", Data: []byte("<p>hi</p>"), Render: room.RenderHTML}}})
	if err != nil {
		t.Fatal(err)
	}

	if out, err := execCmd(t, pinCmd(), "alpha", plain.ID); err != nil || !strings.Contains(out, "pinned "+plain.ID) {
		t.Fatalf("pin = %q, %v", out, err)
	}
	if _, err := execCmd(t, pinCmd(), "alpha", res.Message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := execCmd(t, pinCmd(), "alpha", "msg_nope"); err == nil {
		t.Fatal("pinned an unknown post")
	}
	out, err := execCmd(t, pinsCmd(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], res.Message.ID) || !strings.HasSuffix(lines[0], "a card  [card]") ||
		!strings.HasPrefix(lines[1], plain.ID) || !strings.Contains(lines[1], "alpha:lead  First line") {
		t.Fatalf("pins output:\n%s", out)
	}
	pins, _ := s.Pins(rec.ID)
	if pins[0].By != "alpha" {
		t.Fatalf("by = %q, want the room owner outside a session", pins[0].By)
	}

	out, err = execCmd(t, pinsCmd(), "alpha", "--json")
	var rows []pinRow
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 || rows[1].Post.Text != "First line\nsecond line" {
		t.Fatalf("json = %q, %v", out, err)
	}
	if _, err := execCmd(t, unpinCmd(), "alpha", plain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := execCmd(t, unpinCmd(), "alpha", plain.ID); err != nil {
		t.Fatalf("second unpin: %v", err)
	}
	if out, _ = execCmd(t, pinsCmd(), "alpha"); strings.Contains(out, plain.ID) || !strings.Contains(out, res.Message.ID) {
		t.Fatalf("pins after unpin:\n%s", out)
	}
}

// With no engine, the pin is not changed and dyna room pin exits non-zero
// with the cause (requirement 26).
func TestPinWithoutEngineFailsWithTheCause(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	engineClient = func() *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	s := store()
	rec, err := s.Create(room.CreateInput{Name: "alpha", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execCmd(t, pinCmd(), "alpha", msg.ID); err == nil || !strings.Contains(err.Error(), "the pin was not changed") || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("pin without engine: %v", err)
	}
	if pins, err := s.Pins(rec.ID); err != nil || len(pins) != 0 {
		t.Fatalf("pins = %+v, %v; want none", pins, err)
	}
}

func TestAttachNote(t *testing.T) {
	file := room.Attachment{Name: "a.txt"}
	for _, tc := range []struct {
		atts []room.Attachment
		want string
	}{
		{nil, ""}, {[]room.Attachment{file}, "  [1 file]"}, {[]room.Attachment{file, file}, "  [2 files]"},
	} {
		if got := attachNote(room.Message{Attachments: tc.atts}); got != tc.want {
			t.Fatalf("attachNote(%d files) = %q, want %q", len(tc.atts), got, tc.want)
		}
	}
}
