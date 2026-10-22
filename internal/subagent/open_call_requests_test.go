package subagent

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/instructions"
)

func TestRootSeesCallRequestAndClosureIsADriftNote(t *testing.T) {
	e := newTestEnv(t)
	st := &callrequests.Store{Root: filepath.Dir(e.m.store().root())}
	request, _, err := st.Ask(e.room.ID, "CI needs\n your sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	root := Meta{ID: "ses_20261020-000002-aaaaaaaa", Name: RoleRoot, Room: e.room.Name, RoomID: e.room.ID, Role: RoleRoot, Depth: 1}
	frozen := e.m.sessionInstructions(root, "owner", 3, "")
	want := "- " + request.ID + " · call request from root · " + request.AskedAt.UTC().Format("2006-01-02 15:04 UTC") + " · CI needs your sign-in"
	if !strings.Contains(frozen.Text(), want) || !strings.Contains(frozen.Text(), "dyna call withdraw <id>") {
		t.Fatal(frozen.Text())
	}
	if again := e.m.sessionInstructions(root, "owner", 3, ""); frozen.Hash != again.Hash {
		t.Fatal("unchanged call changed instructions")
	}
	worker := root
	worker.Role, worker.Name = "", "worker"
	if strings.Contains(e.m.sessionInstructions(worker, "owner", 3, "").Text(), "call request from") {
		t.Fatal("worker got root call request")
	}
	if _, _, err := st.Close(e.room.ID, request.ID, "withdrawn", "root"); err != nil {
		t.Fatal(err)
	}
	note := instructions.Note(frozen, e.m.sessionInstructions(root, "owner", 3, ""))
	if !strings.Contains(note, "Open questions: removed") || !strings.Contains(note, request.ID+" · call request from") {
		t.Fatal(note)
	}
}
