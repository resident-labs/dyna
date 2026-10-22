package terminalsvc_test

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/terminalsvc"
	"testing"
	"time"
)

func TestShellJobsCutoffAndCursor(t *testing.T) {
	svc, rooms, sessions := roomTerminalService(t)
	r, err := rooms.Create(room.CreateInput{Name: "paged"})
	if err != nil {
		t.Fatal(err)
	}
	shellStore := terminalsvc.ShellStore(sessions.Root)
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	for _, at := range []time.Time{old, now} {
		job, err := shellStore.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: r.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := shellStore.Save(job, &shells.Exit{Ended: at}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.ShellJobs(r.ID, "", 1)
	if err != nil || len(first.Items) != 1 || !first.Older {
		t.Fatalf("default: %+v %v", first, err)
	}
	page, err := svc.ShellJobs(r.ID, first.Cursor, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Ended == nil || !page.Items[0].Ended.Equal(old) || page.Older {
		t.Fatalf("older: %+v %v", page, err)
	}
}
