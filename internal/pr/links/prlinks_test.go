package links

import (
	"dyna-agent/internal/pr/state"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLinkReaderDoesNotCreateAnUnusedStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	links, err := ForSession(root, "author", "room")
	if err != nil || len(links) != 0 {
		t.Fatalf("empty links: %+v %v", links, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("link reader created state")
	}
}

func TestLinksAreRoomScopedAndIncludeEndedHistory(t *testing.T) {
	root := legacyRoot(t)
	const state = `{"version":1,"records":{
"a":{"roomId":"room","authorSession":"session","reviewerSession":"session","number":17,"state":"open","watching":true},
"b":{"roomId":"room","reviewerSession":"session","number":18,"state":"merged","watching":false},
"c":{"roomId":"another-room","authorSession":"session","number":19,"state":"open","watching":true}
},"reviewers":{"room":{"session":"session","current":{"key":"a"}}}}`
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	links, err := ForSession(root, "session", "room")
	if err != nil || len(links) != 2 || links[0].Number != 17 || len(links[0].Roles) != 2 || links[1].Active() {
		t.Fatalf("links: %+v %v", links, err)
	}
}

func TestOldPoolSlotsDoNotCreateAssignments(t *testing.T) {
	root := legacyRoot(t)
	raw := `{"version":1,"records":{"active":{"roomId":"room","state":"open","watching":true}},"reviewers":{"room":{"session":"stale","slots":[{"session":"first","key":"active"}]}}}`
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "stale"} {
		got, err := ForSession(root, id, "room")
		if err != nil || len(got) != 0 {
			t.Fatalf("old slot created assignment: %+v %v", got, err)
		}
	}
}

func TestReviewerLinksFollowRecordAssignmentAfterVerdict(t *testing.T) {
	for _, verdict := range []string{"", "approve", "changes"} {
		root := legacyRoot(t)
		st := state.State{Version: 1, Records: map[string]*state.Record{"active": {RoomID: "room", State: "open", Watching: true, Reviewer: "reviewer", Verdict: state.ReviewVerdict(verdict)}}}
		b, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(root, "index.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ForSession(root, "reviewer", "room")
		if err != nil || len(got) != 1 || len(got[0].Roles) != 1 || got[0].Roles[0] != "reviewer" {
			t.Fatalf("links=%+v %v", got, err)
		}
	}
}

// legacyRoot returns an empty PR folder inside its own data folder, so the test can
// place a JSON store there for the first read to import.
func legacyRoot(t *testing.T) string {
	t.Helper()
	root := state.Root(t.TempDir())
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
