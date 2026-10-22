package roomsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/notices"
)

func TestNoticesListDismissAndEmitEvents(t *testing.T) {
	e := newEnv(t)
	var events []Event
	version := ""
	emit := func(ev Event) { events = append(events, ev) }
	e.svc.scanNotices(&version, true, emit)
	n, err := e.svc.notices().Post(notices.Notice{Key: "disk", Kind: "disk", Severity: "error", Text: "Disk space is low."})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.scanNotices(&version, false, emit)
	e.svc.scanNotices(&version, false, emit)
	if list, err := e.svc.Notices(); err != nil || len(list) != 1 || list[0].ID != n.ID {
		t.Fatal(list, err)
	}
	if err := e.svc.DismissNotice(n.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.scanNotices(&version, false, emit)
	if list, err := e.svc.Notices(); err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	if len(events) != 2 || events[0].Kind != "notices" || events[1].Kind != "notices" {
		t.Fatal(events)
	}
}

func TestUpdateDynaRunsDynaUpdate(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	e.svc.Notify.Sessions.Executable = filepath.Join(dir, "dyna")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + args + "\n[ \"$1\" = update ] && [ -z \"$FAIL\" ] || { echo 'no release' >&2; exit 1; }\n"
	if err := os.WriteFile(e.svc.Notify.Sessions.Executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UpdateDyna(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(args); string(got) != "update" {
		t.Fatalf("args = %q", got)
	}
	t.Setenv("FAIL", "1")
	if err := e.svc.UpdateDyna(context.Background()); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Fatalf("failure = %v", err)
	}
}

func TestNoticeWatcherSeesDeleteAndNextWriteWithoutMirror(t *testing.T) {
	e := newEnv(t)
	s := e.svc.notices()
	if _, err := s.Post(notices.Notice{Key: "older"}); err != nil {
		t.Fatal(err)
	}
	newest, err := s.Post(notices.Notice{Key: "newest"})
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	events := 0
	emit := func(ev Event) {
		if ev.Kind != "notices" {
			t.Fatalf("event = %+v", ev)
		}
		events++
	}
	e.svc.scanNotices(&version, true, emit)
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	e.svc.scanNotices(&version, false, emit)
	if events != 0 {
		t.Fatal("removing a rollback copy emitted a notice event")
	}
	if err := s.Dismiss(newest.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	e.svc.scanNotices(&version, false, emit)
	if _, err := s.Post(notices.Notice{Key: "next"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	e.svc.scanNotices(&version, false, emit)
	if events != 2 {
		t.Fatalf("delete and next write emitted %d events; want 2", events)
	}
}
