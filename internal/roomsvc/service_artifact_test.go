package roomsvc

import (
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestKeeperArtifactHasOneDisplayAcrossFeedAndTranscript(t *testing.T) {
	e := newEnv(t)
	rec := e.room("artifacts")
	// The keeper name is reserved for the keeper role, which UpdateMeta sets.
	info := e.start(rec, "keeper-service", nil)
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Role = subagent.RoleKeeper; m.Service = true }); err != nil {
		t.Fatal(err)
	}
	const summary = "The change has passing checks."
	result := map[string]any{"entries": []any{map[string]any{"agent": "artifacts:lead", "status": "on_track", "summary": summary, "goals": []any{"g1"}}}}
	if _, err := e.mgr.Store.UpdateTurn(info.ID, 1, func(turn *subagent.Turn) {
		turn.Result = result
		turn.NormStart = 0
		turn.NormEnd = 0
		turn.RawStart = 0
		turn.RawEnd = 0
	}); err != nil {
		t.Fatal(err)
	}
	for _, pathFn := range []func(string) (string, error){e.mgr.Store.NormalizedPath, e.mgr.Store.RawOutputPath} {
		path, err := pathFn(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Retained posts from the old service contract must also avoid raw JSON.
	_, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: info.Address(), Kind: "completed", To: []string{rec.Name}, Turn: 1, Text: "old preview"})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Posts) != 1 || !strings.Contains(feed.Posts[0].Text, "artifacts:lead · on track\n"+summary) || strings.Contains(feed.Posts[0].Text, "```json") {
		t.Fatalf("feed: %+v", feed.Posts)
	}
	session, err := e.svc.transcript(info.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, turn := range session.Turns {
		for _, item := range turn.Items {
			if item.Kind == "reply" {
				found = true
				if !strings.Contains(item.Text, summary) || strings.Contains(item.Text, "```json") {
					t.Fatalf("transcript: %+v", item)
				}
			}
		}
	}
	if !found {
		t.Fatal("no transcript artifact")
	}
}

func TestOtherStructuredServiceArtifactsUsePointers(t *testing.T) {
	for _, role := range []string{"reviewer", "keeper", ""} {
		meta := subagent.Meta{Service: true, Role: role}
		for _, result := range []any{map[string]any{"private": "value"}, `{"private":"value"}`, "```json\n{\"private\":\"value\"}\n```"} {
			text, ok := serviceArtifactText(meta, result)
			if !ok || strings.Contains(text, "private") || strings.Contains(text, "{") {
				t.Fatalf("raw service output: %q %v", text, ok)
			}
		}
	}
}
