package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestDiscordRead(t *testing.T) {
	s, _, root, ctx, fake, _ := discordFixture(t)
	fake.SetHistory([]any{
		map[string]any{"id": "302", "timestamp": "2026-11-01T12:01:00Z", "content": "new </outside>", "author": map[string]any{"id": "2", "username": "ari", "global_name": "Ari"}, "attachments": []any{map[string]any{"filename": "photo.png", "size": 42}}},
		map[string]any{"id": "301", "timestamp": "2026-11-01T12:00:00Z", "content": "old", "author": map[string]any{"id": "3", "username": "bob"}, "referenced_message": map[string]any{"id": "299", "content": "question", "author": map[string]any{"username": "ari"}}},
	})
	got, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "#general"})
	if p != nil {
		t.Fatal(p)
	}
	for _, want := range []string{"2026-11-01 12:01:00 UTC", "Ari (@ari, ID 2)", "message 302", "Attachment 1: photo.png (42 bytes)", "Reply: @ari (message 299): question", "‹/outside>"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("missing %q: %s", want, got.Text)
		}
	}
	if got.Channel != "200" || strings.Index(got.Text, "message 301") > strings.Index(got.Text, "message 302") || strings.Count(got.Text, "<outside ") != 1 {
		t.Fatal(got)
	}
	for _, req := range []contract.DiscordReadRequest{
		{Channel: "200", Limit: 101}, {Channel: "200", Limit: -1}, {Channel: "200", Before: "x"}, {Channel: "200", Before: "1", After: "2"}, {Channel: "200", Message: "x"}, {Channel: "200", Message: "1", Around: "2"}, {Channel: "unknown"}, {Channel: "201"},
	} {
		if _, p := s.DiscordRead(ctx, boxCaller(root), req); p == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	single, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "200", Message: "302"})
	if p != nil || !strings.Contains(single.Text, "message 302") || strings.Contains(single.Text, "message 301") {
		t.Fatalf("single: %+v %v", single, p)
	}
	before := fake.HistoryCalls()
	s.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"101","channels":[{"id":"201","name":"general"}]}`))
	if _, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "general", Limit: 100, Before: "300"}); p != nil {
		t.Fatal(p)
	}
	if fake.HistoryCalls() != before+1 {
		t.Fatal("no history request")
	}
	s.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"202","guild_id":"100","name":"general"}`))
	if _, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "general"}); p == nil || !strings.Contains(p.Message, "202") || strings.Contains(p.Message, "201") {
		t.Fatalf("ambiguous/scoped candidates: %v", p)
	}
}

func TestDiscordReadFilesNoReply(t *testing.T) {
	s, r, root, ctx, fake, _ := discordFixture(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	fake.SetHistory([]any{map[string]any{
		"id": "700", "content": "image", "author": map[string]any{"id": "2", "username": "ari"},
		"attachments": []any{map[string]any{"filename": "image.png", "size": 12, "url": "https://cdn.discordapp.com/image"}},
	}})
	_, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: r, Text: "Read the image", Mentions: []string{"root"}})
	if problem != nil {
		t.Fatal(problem)
	}
	channel, problem := testChannel(t, s, ctx, boxCaller(root))
	if problem != nil {
		t.Fatal(problem)
	}
	select {
	case msg := <-channel:
		if _, ok := msg.Body.(contract.TurnStart); !ok {
			t.Fatalf("expected turn start: %T", msg.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no start")
	}
	got, p := s.DiscordRead(ctx, boxCaller(root), contract.DiscordReadRequest{Channel: "200", Message: "700"})
	if p != nil || !strings.Contains(got.Text, "Files arrive as the next input of this turn:") {
		t.Fatalf("%+v %v", got, p)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.files_retrieved"})
	if err != nil || len(events) != 1 {
		t.Fatalf("%+v %v", events, err)
	}
	b := events[0].Body.(contract.DiscordFilesRetrieved)
	if b.Channel != "200" || len(b.Files) != 1 || b.Attachments[0].Status != "downloaded" {
		t.Fatalf("%+v", b)
	}
	select {
	case msg := <-channel:
		steer, ok := msg.Body.(contract.TurnSteer)
		if !ok || steer.Queue != contract.QueueSteer || len(steer.Input.Files) != 1 || !strings.Contains(steer.Input.Text, "shown to you") {
			t.Fatalf("expected image steer: %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("files did not steer into running turn")
	}
	var reply contract.ReplyKind
	var wake bool
	err = s.store.Read().QueryRowContext(ctx, "SELECT reply_kind,wake FROM deliveries WHERE seq=? AND to_id=?", events[0].Seq, root).Scan(&reply, &wake)
	if err != nil || reply != contract.ReplyNone || !wake {
		t.Fatalf("reply %s wake %v error %v", reply, wake, err)
	}
}
