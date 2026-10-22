package subagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestDismissPendingCommandRequiresOwnerConfirmation(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	now := time.Now()
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "quiet", Room: work.Name, RoomID: work.ID, Created: now, LastActivity: now}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	msg, err := m.RoomStore().Post(meta.RoomID, room.PostInput{Wake: true, From: "work:reporter", To: []string{meta.Address()}, Text: "unread report"})
	if err != nil {
		t.Fatal(err)
	}
	serveSessionControls(t, m)
	call := func(confirm bool) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		args := []string{"dismiss-pending", meta.ID, msg.ID}
		if confirm {
			args = append(args, "--confirm")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := call(false); err == nil {
		t.Fatal("accepted unconfirmed dismissal")
	}
	t.Setenv(mode.EnvSession, meta.ID)
	if _, err := call(true); err == nil {
		t.Fatal("worker bypassed owner authority")
	}
	t.Setenv(mode.EnvSession, "")
	if out, err := call(true); err != nil || !strings.Contains(out, "history preserved") {
		t.Fatalf("dismiss: %s %v", out, err)
	}
	queue, err := m.Store.SessionQueue(m.RoomStore(), meta.ID)
	if err != nil || len(queue) != 1 || queue[0].State != "dismissed" {
		t.Fatalf("queue: %+v %v", queue, err)
	}
}
