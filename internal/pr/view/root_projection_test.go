package view_test

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestSharedPRScanScopesRevisionToStoreRoot(t *testing.T) {
	e := newEnv(t)
	rec := e.room("root-scope")
	oldRoot := e.mgr.Store.Root
	newRoot := filepath.Join(t.TempDir(), "sessions")
	id := "ses_20261021-120000-00000001"
	meta := subagent.Meta{ID: id, Name: "worker", RoomID: rec.ID, Room: rec.Name, Created: time.Unix(1, 0)}
	recordtest.Write(t, oldRoot, id, meta)
	recordtest.Write(t, newRoot, id, meta)
	other := &subagent.Store{Root: newRoot}
	for _, store := range []*subagent.Store{e.mgr.Store, other} {
		if _, err := store.MembersRevision(); err != nil {
			t.Fatal(err)
		}
	}
	a, err := e.mgr.Store.MembersRevision()
	if err != nil {
		t.Fatal(err)
	}
	b, err := other.MembersRevision()
	if err != nil || a != b {
		t.Fatalf("fixture member revisions differ: %q, %q, %v", a, b, err)
	}
	for _, source := range []struct{ root, key string }{{oldRoot, "old"}, {newRoot, "new"}} {
		testfixture.SaveState(t, filepath.Join(filepath.Dir(source.root), "prs"), watch.State{Version: 1, Records: map[string]*watch.Record{
			source.key: {Key: source.key, Kind: "github", RoomID: rec.ID, State: "open", Title: source.key},
		}})
	}
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.PRs.Scan(signatures, work, &version, true, func(string, string, *listchange.Change, bool) {})
	before := version
	e.mgr.Store.Root = newRoot
	t.Cleanup(func() { e.mgr.Store.Root = oldRoot })
	added, removed := false, false
	e.svc.PRs.Scan(signatures, work, &version, false, func(kind, roomID string, change *listchange.Change, _ bool) {
		if kind != "changes" || roomID != rec.ID || change == nil {
			return
		}
		added = added || change.ID == "new" && change.Op == "upsert"
		removed = removed || change.ID == "old" && change.Op == "delete"
	})
	if !added || !removed || version != before {
		t.Fatalf("same-revision root switch: added=%v, removed=%v, revision %q -> %q", added, removed, before, version)
	}
}
