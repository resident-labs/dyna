package roomsvc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestComposerReferencesSurviveStorageAndForwarding(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	refs := []room.Reference{
		{Kind: "pr", Number: float64(42), Title: "Fix delivery", State: "open", URL: "https://github.com/acme/repo/pull/42"},
		{Kind: "issue", Number: float64(43), Title: "Missing files", State: "closed", URL: "https://github.com/acme/repo/issues/43"},
		{Kind: "room_issue", Number: "issue_abc", Title: "Check composer", State: "open", URL: "#room=" + rec.ID + "&view=issues"},
	}
	// A reference-only post must pass both composer and delivery validation.
	res, err := e.svc.SendWithReferences(e.ctx, rec.ID, "@root", "", "", refs)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := e.svc.Notify.Rooms.Message(rec.ID, res.Post.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Text != "" || !reflect.DeepEqual(stored.References, refs) {
		t.Fatalf("stored post = %+v", stored)
	}
	encoded, err := json.Marshal(res.Post.Message)
	if err != nil || !strings.Contains(string(encoded), `"references":`) {
		t.Fatalf("wire post = %s, %v", encoded, err)
	}
	forwarded, err := e.svc.Send(e.ctx, rec.ID, "@root check these", "", stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if forwarded.Post.Message.Forward == nil || !reflect.DeepEqual(forwarded.Post.Message.Forward.References, refs) {
		t.Fatalf("forward = %+v", forwarded)
	}
	frame := forwarded.Post.Message.Frame(room.FrameOptions{Room: rec.Name})
	for _, want := range []string{
		"PR #42: Fix delivery (open, https://github.com/acme/repo/pull/42)",
		"Issue #43: Missing files (closed, https://github.com/acme/repo/issues/43)",
		"Room issue #issue_abc: Check composer (open, #room=" + rec.ID + "&view=issues)",
	} {
		if !strings.Contains(frame, "> "+want) {
			t.Errorf("frame missing %q: %s", want, frame)
		}
	}
	if _, err := e.svc.SendWithReferences(e.ctx, rec.ID, "/mock task", "", "", refs); err == nil {
		t.Fatal("references silently lost in spawn")
	}
	invalid := []room.Reference{{Kind: "room_issue", Number: "issue_abc", Title: "Check composer", URL: "#room=alpha&view=files"}}
	if _, err := e.svc.SendWithReferences(e.ctx, rec.ID, "@root", "", "", invalid); err == nil {
		t.Fatal("unrecognized room fragment accepted")
	}
}
