package roomsvc

import (
	"errors"
	"testing"

	"dyna-agent/internal/subagent"
)

// A session record caught mid-write fails one roster read. The avatars of that
// response must come from the last good roster, not from an empty one.
func TestFailedRosterReadKeepsTheLastRosterForAvatars(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	if all, _ := e.svc.snapshot(); len(all) == 0 {
		t.Fatal("the first read found no sessions")
	}
	read := readSnapshotSession
	readSnapshotSession = func(func(string) (subagent.Info, error), string) (subagent.Info, error) {
		return subagent.Info{}, errors.New("record is being written")
	}
	t.Cleanup(func() { readSnapshotSession = read })
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	e.svc.SessionChanged(lead.ID)
	all, rev := e.svc.snapshot()
	if book := e.svc.avatarBook(rec.ID, all, rev); book.byID[lead.ID] == "" {
		t.Fatalf("a failed read left session %s with no avatar", lead.ID)
	}
}
