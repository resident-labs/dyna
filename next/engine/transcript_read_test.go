package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptReadOwnerAndTurn(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT seq FROM deliveries WHERE to_kind='session' AND to_id=? LIMIT 1", caller.Session).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:00Z\",\"message\":{\"role\":\"user\",\"deliveryId\":\"%d\"}}\n{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:01Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"c\",\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}]}}\n", seq)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	req := contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a"}
	if _, e := s.TranscriptRead(ctx, caller, req); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("session read: %v", e)
	}
	got, e := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 0 {
		t.Fatalf("read = %+v %v", got, e)
	}
	req.Turn = "other"
	got, e = s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 0 {
		t.Fatalf("filter = %+v %v", got, e)
	}
}

func TestTranscriptTurnPageAndFullOutput(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	delivery := wake.Seq
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var input contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", delivery).Scan(&input); err != nil {
			return err
		}
		event := contract.Event{ID: "transcript-start", Room: session.Room, From: caller, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_a", Inputs: []contract.EventID{input}}}
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", caller.Session, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, delivery)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	post, postErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "sent during turn", ToolCallID: "post-call"})
	if postErr != nil {
		t.Fatal(postErr)
	}
	event, err := s.store.Event(ctx, post.Seq)
	if err != nil || event.Body.(contract.PostCreated).Turn != "turn_a" {
		t.Fatalf("post=%+v err=%v", event, err)
	}
	for i := 0; i < maxTranscriptCallPosts; i++ {
		if _, problem := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "another", ToolCallID: "post-call"}); problem != nil {
			t.Fatal(problem)
		}
	}
	other, otherErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "another call", ToolCallID: "call1"})
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	unclaimed, unclaimedErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "unclaimed"})
	if unclaimedErr != nil {
		t.Fatal(unclaimedErr)
	}
	output := strings.Repeat("x", 1200) + " token=secretvalue"
	data := fmt.Sprintf(`{"type":"message","id":"user","message":{"role":"user","deliveryId":"%d"}}
{"type":"message","id":"assistant","message":{"role":"assistant","content":[{"type":"toolCall","id":"call1","name":"bash","arguments":{}},{"type":"toolCall","id":"post-call","name":"bash","arguments":{"command":"dyna post hello"}}]}}
{"type":"message","id":"result","message":{"role":"toolResult","toolCallId":"call1","content":[{"type":"text","text":%q}]}}
{"type":"message","id":"post-result","message":{"role":"toolResult","toolCallId":"post-call","content":[{"type":"text","text":%q}]}}
`, delivery, output, post.Event)
	ownerLive, roomLive := make(chan contract.Event, 1), make(chan contract.Event, 1)
	s.mu.Lock()
	s.live[ownerLive] = ""
	s.live[roomLive] = session.Room
	s.mu.Unlock()
	split := strings.Index(data, "\n") - 2
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", 0, data[:split]); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		t.Fatalf("incomplete record sent notice: %+v", event)
	default:
	}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(split), data[split:]); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		changed, ok := event.Body.(contract.TranscriptChanged)
		if !ok || changed.Session != caller.Session || changed.Turn != "turn_a" {
			t.Fatalf("notice=%+v", event)
		}
	default:
		t.Fatal("owner did not receive transcript notice")
	}
	select {
	case event := <-roomLive:
		t.Fatalf("room received owner-only notice: %+v", event)
	default:
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE turns SET ended_at=1 WHERE id='turn_a'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)), " "); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		if event.Body.(contract.TranscriptChanged).Turn != "turn_a" {
			t.Fatal(event)
		}
	default:
		t.Fatal("late append did not notify owner")
	}
	ownerLive <- contract.Event{}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)+1), " "); problem != nil {
		t.Fatal(problem)
	}
	<-ownerLive
	if _, ok := <-ownerLive; ok {
		t.Fatal("full owner stream did not close for reconnect")
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	page, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	inputEvent, err := s.store.Event(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if problem != nil || len(page.Turns) != 1 || len(page.Turns[0].Inputs) != 1 || !page.Turns[0].Inputs[0].At.Equal(inputEvent.Time) || page.Turns[0].Inputs[0].Home == nil || page.Turns[0].Inputs[0].Home.Kind != contract.ConversationDM || page.Turns[0].Inputs[0].Seq != delivery || len(page.Turns[0].Items) != 3 || len(page.Turns[0].Items[0].Posts) != 1 || page.Turns[0].Items[0].Posts[0].Event != other.Event || len(page.Turns[0].Items[1].Posts) != maxTranscriptCallPosts || page.Turns[0].Items[1].Posts[0].Event != post.Event || page.Turns[0].Items[1].PostsOmitted != 1 || page.Turns[0].Items[2].Event != unclaimed.Event || !page.Turns[0].Items[0].More || len(page.Turns[0].Items[0].Output) != 1024 {
		t.Fatalf("page=%+v error=%v", page, problem)
	}
	for i := 0; i < 64; i++ {
		if _, problem := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "more", ToolCallID: "post-call"}); problem != nil {
			t.Fatal(problem)
		}
	}
	bounded, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a"})
	if problem != nil || !bounded.Turns[0].Tail || bounded.Turns[0].NextPost != 64 {
		t.Fatalf("bounded tail=%+v error=%v", bounded, problem)
	}
	continued, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Tail: true, PostIndex: bounded.Turns[0].NextPost})
	if problem != nil || continued.Turns[0].More {
		t.Fatalf("continued tail=%+v error=%v", continued, problem)
	}
	full, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Item: "call1"})
	if problem != nil || strings.Contains(full.Chunk, "secretvalue") || !strings.Contains(full.Chunk, "[masked]") {
		t.Fatalf("full=%+v error=%v", full, problem)
	}
}

func TestTranscriptPostPreviewBoundsJSONEscapes(t *testing.T) {
	checks := make([]contract.VisualCheck, 16)
	for i := range checks {
		checks[i].Detail = strings.Repeat("\x01", 4096)
	}
	attachments := make([]contract.Attachment, 16)
	for i := range attachments {
		attachments[i].Name = strings.Repeat("\x01", 255)
		attachments[i].Media = strings.Repeat("\x01", 4096)
	}
	attachments[0].Visual = &contract.VisualCard{Checks: checks}
	event := contract.Event{Body: contract.PostCreated{Text: strings.Repeat("\x01", 8192), Attachments: attachments}}
	item := contract.TranscriptItem{Arguments: strings.Repeat("\x01", 8192)}
	applyTranscriptPost(&item, event)
	data, err := json.Marshal(item)
	if err != nil || len(data) > 140<<10 {
		t.Fatalf("preview bytes=%d, err=%v", len(data), err)
	}
}
