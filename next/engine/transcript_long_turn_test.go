package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// startTranscriptTurn starts turn_a, which read the wake post and then extra posts from the owner, and returns the wake's delivery.
func startTranscriptTurn(t *testing.T, s *Service, ctx context.Context, caller contract.Principal, extra int, attachments []contract.Attachment) int64 {
	t.Helper()
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	home := contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}
	wake, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: session.Room, Home: home, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var first contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", wake.Seq).Scan(&first); err != nil {
			return err
		}
		inputs := []contract.EventID{first}
		for i := 0; i < extra; i++ {
			id := contract.EventID(fmt.Sprintf("input-%d", i))
			event := contract.Event{ID: id, Room: session.Room, From: contract.Principal{Kind: contract.PrincipalOwner}, Time: time.Unix(int64(10+i), 0), Body: contract.PostCreated{Text: "more", Home: home, Attachments: attachments}}
			if _, err := s.store.AppendEvent(tx, event); err != nil {
				return err
			}
			inputs = append(inputs, id)
		}
		started := contract.Event{ID: "turn-start", Room: session.Room, From: caller, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_a", Inputs: inputs}}
		seq, err := s.store.AppendEvent(tx, started)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", caller.Session, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, wake.Seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return wake.Seq
}

func TestLongTurnFirstPageCarriesItemsAndInputsPageByCount(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	delivery := startTranscriptTurn(t, s, ctx, caller, 119, nil) // 120 inputs with the wake
	data := fmt.Sprintf(`{"type":"message","id":"u","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}
{"type":"message","id":"a","timestamp":"2026-01-01T00:00:05Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"plan"},{"type":"toolCall","id":"c","name":"bash","arguments":{}}]}}
`, delivery)
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); problem != nil {
		t.Fatal(problem)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	first, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	turn := first.Turns[0]
	if len(turn.Inputs) != 50 || turn.After == "" || !turn.More || len(turn.Items) != 2 {
		t.Fatalf("first page has %d inputs, after=%q more=%v and %d items, want 50, a cursor, more and 2 items", len(turn.Inputs), turn.After, turn.More, len(turn.Items))
	}
	total, pages, after := len(turn.Inputs), 1, turn.After
	for after != "" {
		next, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", AfterItem: after})
		if problem != nil {
			t.Fatal(problem)
		}
		page := next.Turns[0]
		if len(page.Items) != 0 || len(page.Inputs) > 50 {
			t.Fatalf("continuation has %d inputs and %d items, want at most 50 and none", len(page.Inputs), len(page.Items))
		}
		total, after = total+len(page.Inputs), page.After
		if pages++; pages > 3 {
			t.Fatal("input cursor did not advance")
		}
	}
	if total != 120 || pages != 3 {
		t.Fatalf("read %d inputs in %d pages, want 120 in 3", total, pages)
	}
}

func TestLongTurnInputPagesStopAtTheirByteBudget(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	attachments := make([]contract.Attachment, 16)
	for i := range attachments {
		attachments[i] = contract.Attachment{Name: strings.Repeat("n", 120), Media: "image/png", SHA256: strings.Repeat("a", 64), Bytes: 42}
	}
	startTranscriptTurn(t, s, ctx, caller, 40, attachments)
	page, problem := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.TranscriptReadRequest{Session: caller.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	inputs, _ := json.Marshal(page.Turns[0].Inputs)
	if len(page.Turns[0].Inputs) >= 50 || len(inputs) > 48<<10 || page.Turns[0].After == "" {
		t.Fatalf("page has %d inputs in %d bytes with cursor %q, want fewer than 50 in at most 48 KiB", len(page.Turns[0].Inputs), len(inputs), page.Turns[0].After)
	}
}

// Shell posts carry no tool call, so the turn's items do not hold them. They belong between the items they were sent between.
func TestPostsFromAShellHaveTheirPlaceAmongTheItems(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	delivery := startTranscriptTurn(t, s, ctx, caller, 0, nil)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	postAt := map[string]time.Duration{"during-first-call": 15 * time.Second, "between-calls": 45 * time.Second, "after-everything": 90 * time.Second}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range []string{"during-first-call", "between-calls", "after-everything"} {
			event := contract.Event{ID: contract.EventID(id), Room: session.Room, From: caller, Time: base.Add(postAt[id]), Body: contract.PostCreated{Text: id, Turn: "turn_a", Home: contract.Conversation{Kind: contract.ConversationFeed}}}
			if _, err := s.store.AppendEvent(tx, event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"type":"message","id":"u","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}
{"type":"message","id":"a1","timestamp":"2026-01-01T00:00:10Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"first","name":"bash","arguments":{}}]}}
{"type":"message","id":"r1","timestamp":"2026-01-01T00:00:20Z","message":{"role":"toolResult","toolCallId":"first","content":[]}}
{"type":"message","id":"a2","timestamp":"2026-01-01T00:00:30Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"next"},{"type":"toolCall","id":"second","name":"bash","arguments":{}}]}}
{"type":"message","id":"a3","timestamp":"2026-01-01T00:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}
`, delivery)
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); problem != nil {
		t.Fatal(problem)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	// Items are first(0), thinking(1), second(2), text(3). A post takes the index of the first item that began after it.
	want := map[string]int{"during-first-call": 1, "between-calls": 3, "after-everything": 4}
	check := func(label string, items []contract.TranscriptItem) {
		t.Helper()
		found := 0
		for _, item := range items {
			if item.Kind != contract.TranscriptPost {
				continue
			}
			found++
			if item.Index != want[item.Text] {
				t.Errorf("%s: post %q has index %d, want %d", label, item.Text, item.Index, want[item.Text])
			}
		}
		if found != 3 {
			t.Errorf("%s: found %d posts, want 3", label, found)
		}
	}
	first, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	check("first page", first.Turns[0].Items)
	var newest int64
	for _, item := range first.Turns[0].Items {
		if item.Kind == contract.TranscriptPost && item.Seq > newest {
			newest = item.Seq
		}
	}
	posts, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Page: contract.TranscriptPageMandatoryPosts, Cursor: int(newest)})
	if problem != nil {
		t.Fatal(problem)
	}
	check("posts page", posts.Turns[0].Items)
}
