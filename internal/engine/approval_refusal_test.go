package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/store/storetest"
)

func TestApprovalRefusalPostsTypedEvent(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "refusal-test"})
	if err != nil {
		t.Fatal(err)
	}
	rec = roomfixture.Repository(t, s.rooms.Notify.Rooms, rec.ID, "github.com", "o/r", "read-only")
	actor := authority.Actor{Role: authority.Session, Session: "ses_20261020-000000-abcde111", Room: rec.ID}
	_, err = s.approvalRequest(authority.WithActor(t.Context(), actor), approvals.RequestInput{Argv: []string{"issue", "create", "--repo", "o/r"}, Cwd: rec.Repo})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error = %v", err)
	}
	messages, err := s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if msg.Kind != string(room.FamilyGitHubWrite) {
			continue
		}
		raw, err := msg.Payload.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var ev room.GitHubWriteRefusedEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Requester != actor.Session || ev.Reason != room.GitHubWriteReadOnly || ev.Rule != "read-only" || ev.Repository != "github.com/o/r" || ev.Verb != "issue.create" {
			t.Fatalf("event = %+v", ev)
		}
		return
	}
	t.Fatal("no typed refusal event")
}
