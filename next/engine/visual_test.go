package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestVisualVersionsAndServing(t *testing.T) {
	s, room, session, ctx := setup(t)
	s.SetFilesDir(filepath.Join(t.TempDir(), "files"))
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
	file.Visual = &contract.VisualCard{ID: "spoof", Version: 99, AllowNet: true, Height: 120}
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
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'unsafe-inline' https:; connect-src https: data: blob:;") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "style-src 'unsafe-inline' https: data: blob:;") {
			t.Fatalf("response: %d %v", rec.Code, rec.Header())
		}
	}
	rec = httptest.NewRecorder()
	s.Files(author, "other").ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/visual/"+string(first.Event), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other room: %d", rec.Code)
	}
}
