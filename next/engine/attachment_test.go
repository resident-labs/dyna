package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A real PNG signature, which is all the frame reads of a file.
const pngHead = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func uploadFile(t *testing.T, s *Service, room contract.RoomID, name, media, body string) contract.Attachment {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/files?name="+name, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", media)
	rec := httptest.NewRecorder()
	s.Files(contract.Principal{Kind: contract.PrincipalOwner}, room).ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	return file
}

// A mention can deliver a post to another room. That session reads this file and no other session there does.
func TestDeliveryGrantsThatPostsFileAcrossRooms(t *testing.T) {
	s, roomA, sessionA, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	roomB, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	named, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomB.Room, Name: "named", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	bystander, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomB.Room, Name: "bystander", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	file := uploadFile(t, s, roomA, "note.txt", "text/plain", "for named only")
	sender := contract.Principal{Kind: contract.PrincipalSession, Session: sessionA}
	if _, problem := s.Post(ctx, sender, contract.PostRequest{Room: roomA, Text: "see this", Attachments: []contract.Attachment{file}, Mentions: []string{"other:named"}}); problem != nil {
		t.Fatal(problem)
	}
	read := func(session contract.SessionID) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Files(contract.Principal{Kind: contract.PrincipalSession, Session: session}, roomB.Room).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/"+file.SHA256, nil))
		return rec
	}
	if rec := read(named.Session); rec.Code != http.StatusOK || rec.Body.String() != "for named only" {
		t.Fatalf("named: %d %s", rec.Code, rec.Body)
	}
	if rec := read(bystander.Session); rec.Code != http.StatusNotFound {
		t.Fatalf("bystander: %d %s", rec.Code, rec.Body)
	}
}
