package view_test

import (
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestSharedPRHistoryExpiresWithoutAWrite(t *testing.T) {
	e := newEnv(t)
	rec := e.room("history-clock")
	now := time.Now()
	e.svc.PRs.Now = func() time.Time { return now }
	root := e.svc.PRs.Watcher().Store.Root
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"closed": {Key: "closed", Kind: "github", RoomID: rec.ID, State: "closed", ClosedAt: now.Add(-23 * time.Hour)},
	}})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.PRs.Scan(signatures, work, &version, true, func(string, string, *listchange.Change, bool) {})
	before := version
	page, err := e.svc.PRs.Changes(rec.ID, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("initial recent page = %+v, %v", page, err)
	}
	now = now.Add(2 * time.Hour)
	leftHead := false
	e.svc.PRs.Scan(signatures, work, &version, false, func(kind, roomID string, change *listchange.Change, _ bool) {
		if kind == "changes" && roomID == rec.ID && change != nil && change.ID == "closed" && change.InHead != nil && !*change.InHead {
			leftHead = true
		}
	})
	page, err = e.svc.PRs.Changes(rec.ID, "", 10)
	if err != nil || len(page.Items) != 0 || !page.Older || !leftHead || version != before {
		t.Fatalf("expired recent page = %+v, event=%v, version %q -> %q, %v", page, leftHead, before, version, err)
	}
}
