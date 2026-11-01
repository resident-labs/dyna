package engine

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptImageRoute(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	seq := wake.Seq
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var input contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", seq).Scan(&input); err != nil {
			return err
		}
		event := contract.Event{ID: "image-start", Room: session.Room, From: caller, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_a", Inputs: []contract.EventID{input}}}
		started, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", caller.Session, started); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\r\n\x1a\nbytes"
	encodedImage := base64.StdEncoding.EncodeToString([]byte(png))
	data := fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}`, seq) + "\n" +
		`{"type":"message","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call/1","name":"read","arguments":{"path":"/w/a.png"}},{"type":"toolCall","id":"text","name":"read","arguments":{"path":"/w/a.txt"}}]}}` + "\n" +
		`{"type":"message","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"call/1","content":[{"type":"image","data":"` + encodedImage + `","mimeType":"image/png"}]}}` + "\n" +
		`{"type":"message","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"text","content":[{"type":"text","text":"plain"}]}}` + "\n"
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	get := func(who contract.Principal, session, turn, item string) *httptest.ResponseRecorder {
		query := url.Values{"session": {session}, "turn": {turn}, "item": {item}}
		rec := httptest.NewRecorder()
		s.Files(who, "").ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/transcript?"+query.Encode(), nil))
		return rec
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	rec := get(owner, string(caller.Session), "turn_a", "call/1")
	if rec.Code != 200 || rec.Body.String() != png || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("image: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	read, e := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a"})
	if e != nil || len(read.Steps) != 2 || read.Steps[0].Image == nil || read.Steps[0].Image.Item != "call/1" || read.Steps[1].Image != nil {
		t.Fatalf("steps=%+v %v", read.Steps, e)
	}
	if encoded, _ := json.Marshal(read); bytes.Contains(encoded, []byte(encodedImage)) {
		t.Fatal("transcript read carries the image bytes")
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"session":      get(caller, string(caller.Session), "turn_a", "call/1"),
		"no image":     get(owner, string(caller.Session), "turn_a", "text"),
		"other turn":   get(owner, string(caller.Session), "other", "call/1"),
		"no such item": get(owner, string(caller.Session), "turn_a", "missing"),
		"no session":   get(owner, "missing", "turn_a", "call/1"),
	} {
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
}
