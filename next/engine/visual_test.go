package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestVisualReplyRequiresCurrentVersion(t *testing.T) {
	s, room, session, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	author := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	req := httptest.NewRequest("POST", "/v1/files?name=card.html", bytes.NewBufferString("<title>Card</title>"))
	req.Header.Set("Content-Type", "text/html")
	rec := httptest.NewRecorder()
	s.Files(author, room).ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatal(rec.Code)
	}
	file.Visual = &contract.VisualCard{Height: 120}
	first, problem := s.Post(ctx, author, contract.PostRequest{Room: room, Text: "card", Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	second, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "update", Home: contract.Conversation{Kind: contract.ConversationDM, Session: session}, VisualUpdate: first.Event, Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "reply", VisualReply: first.Event})
	if problem == nil || problem.Code != contract.ErrVisualSuperseded || problem.Visual == nil || problem.Visual.Event != second.Event || problem.Visual.Version != 2 {
		t.Fatalf("stale reply: %+v", problem)
	}
	if _, problem = s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "reply", VisualReply: second.Event}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem = s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "reply", VisualReply: "missing"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("missing visual: %+v", problem)
	}
}

func visualReplyFixture(t *testing.T) (*Service, contract.RoomID, contract.SessionID, contract.PostResponse, context.Context) {
	t.Helper()
	s, roomID, root, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomID, Parent: root, Name: "author", Profile: "test", Goal: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	author := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	req := httptest.NewRequest("POST", "/v1/files?name=card.html", bytes.NewBufferString("<title>Card</title>"))
	req.Header.Set("Content-Type", "text/html")
	rec := httptest.NewRecorder()
	s.Files(author, roomID).ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatal(rec.Code)
	}
	file.Visual = &contract.VisualCard{Height: 120}
	first, problem := s.Post(ctx, author, contract.PostRequest{Room: roomID, Text: "card", Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	second, problem := s.Post(ctx, owner, contract.PostRequest{Room: roomID, Text: "update", VisualUpdate: first.Event, Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	return s, roomID, child.Session, second, ctx
}

func TestVisualReplyUsesCardSurface(t *testing.T) {
	s, roomID, _, card, ctx := visualReplyFixture(t)
	reply, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: roomID, Text: "answer", VisualReply: card.Event})
	if problem != nil {
		t.Fatal(problem)
	}
	var surface contract.Surface
	var replyTo contract.EventID
	var kind contract.ReplyKind
	var target contract.PrincipalKind
	err := s.store.Read().QueryRowContext(ctx, "SELECT surface,reply_to,reply_kind,reply_target_kind FROM deliveries WHERE seq=?", reply.Seq).Scan(&surface, &replyTo, &kind, &target)
	if err != nil || surface != contract.SurfaceCard || replyTo != card.Event || kind != contract.ReplyFinal || target != contract.PrincipalOwner {
		t.Fatalf("card delivery: %s %s %s %s %v", surface, replyTo, kind, target, err)
	}
}

func TestVisualReplyTargetsOriginalAuthor(t *testing.T) {
	s, roomID, author, card, ctx := visualReplyFixture(t)
	reply, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: roomID, Text: "answer", VisualReply: card.Event})
	if problem != nil {
		t.Fatal(problem)
	}
	var recipient contract.SessionID
	err := s.store.Read().QueryRowContext(ctx, "SELECT to_id FROM deliveries WHERE seq=?", reply.Seq).Scan(&recipient)
	if err != nil || recipient != author {
		t.Fatalf("recipient: %s, want %s: %v", recipient, author, err)
	}
}

func TestVisualOriginalAuthorLookup(t *testing.T) {
	s, roomID, author, card, ctx := visualReplyFixture(t)
	event, err := s.store.Event(ctx, card.Seq)
	if err != nil {
		t.Fatal(err)
	}
	id := event.Body.(contract.PostCreated).Attachments[0].Visual.ID
	page, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: roomID, Event: id, Limit: 1})
	if problem != nil || len(page.Events) != 1 || page.Events[0].ID != id || page.Events[0].From.Session != author || page.Names[author] != "author" {
		t.Fatalf("original event: %+v %v", page, problem)
	}
}

func TestVisualVersionsAndServing(t *testing.T) {
	s, room, session, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	author := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := httptest.NewRequest("POST", "/v1/files?name=card.html", bytes.NewBufferString("<title>Card</title>"))
	req.Header.Set("Content-Type", "text/html")
	rec := httptest.NewRecorder()
	s.Files(author, room).ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatal(rec.Code)
	}
	file.Visual = &contract.VisualCard{ID: "spoof", Version: 99, Height: 120}
	for _, height := range []int{0, 119, 1201} {
		file.Visual.Height = height
		if _, problem := s.Post(ctx, author, contract.PostRequest{Room: room, Text: "summary", Attachments: []contract.Attachment{file}}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("height %d: %v", height, problem)
		}
	}
	file.Visual.Height = 120
	if _, problem := s.Post(ctx, author, contract.PostRequest{Room: room, Attachments: []contract.Attachment{file}}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("empty summary: %v", problem)
	}
	first, problem := s.Post(ctx, author, contract.PostRequest{Room: room, Text: "summary", Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	other, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "other", Profile: "test", Goal: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: other.Session}, contract.PostRequest{Room: room, Text: "summary", VisualUpdate: first.Event, Attachments: []contract.Attachment{file}})
	if problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("other author: %v", problem)
	}
	file.Visual.Height = 1200
	file.Visual.AllowNet = true
	second, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "summary", VisualUpdate: first.Event, Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	for i, result := range []contract.PostResponse{first, second} {
		event, err := s.store.Event(ctx, result.Seq)
		if err != nil {
			t.Fatal(err)
		}
		card := event.Body.(contract.PostCreated).Attachments[0].Visual
		if card.ID != first.Event || card.Version != i+1 {
			t.Fatalf("card: %+v", card)
		}
		rec := httptest.NewRecorder()
		s.Files(author, room).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/visual/"+string(result.Event), nil))
		csp := rec.Header().Get("Content-Security-Policy")
		if rec.Code != http.StatusOK || !strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "https:") != (i == 1) || (i == 1 && (!strings.Contains(csp, "script-src 'unsafe-inline' https:; connect-src https: data: blob:;") || !strings.Contains(csp, "style-src 'unsafe-inline' https: data: blob:;"))) {
			t.Fatalf("response: %d %v", rec.Code, rec.Header())
		}
	}
	rec = httptest.NewRecorder()
	s.Files(author, "other").ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/visual/"+string(first.Event), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other room: %d", rec.Code)
	}
}
