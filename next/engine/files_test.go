package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestFileRoomAccess(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, nil, nil, time.Now)
	s.SetLayout(box.Layout{Data: dir})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES ('a','a',0,1,1),('b','b',0,1,1)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(query string) *httptest.ResponseRecorder {
		upload := httptest.NewRequest("POST", "/v1/files?name=note.txt"+query, bytes.NewBufferString("hello"))
		upload.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		s.Files(owner, "").ServeHTTP(rec, upload)
		return rec
	}
	for query, status := range map[string]int{"": http.StatusBadRequest, "&room=missing": http.StatusNotFound} {
		if rec := send(query); rec.Code != status {
			t.Fatalf("upload %q: %d", query, rec.Code)
		}
	}
	rec := send("&room=a")
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatalf("upload: %d", rec.Code)
	}
	if _, err := os.Stat(s.layout.File("a", file.SHA256)); err != nil {
		t.Fatal(err)
	}
	_, err = st.Post(context.Background(), contract.Event{ID: "e", Room: "a", Time: time.Now(), From: owner, Body: contract.PostCreated{Text: "hi", Attachments: []contract.Attachment{file}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		caller contract.Principal
		room   contract.RoomID
		status int
	}{
		{owner, "", http.StatusOK},
		{contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, "a", http.StatusOK},
		{contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, "b", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		s.Files(tc.caller, tc.room).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/"+file.SHA256, nil))
		if rec.Code != tc.status {
			t.Fatalf("room %s: %d", tc.room, rec.Code)
		}
		if tc.status == http.StatusOK && rec.Header().Get("Content-Security-Policy") != "sandbox" {
			t.Fatal(rec.Header())
		}
	}
	file.Media = "text/html"
	_, err = st.Post(context.Background(), contract.Event{ID: "e2", Room: "a", Time: time.Now(), From: owner, Body: contract.PostCreated{Text: "hi", Attachments: []contract.Attachment{file}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.Files(owner, "").ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/"+file.SHA256, nil))
	if rec.Header().Get("Content-Type") != "text/html" || rec.Header().Get("Content-Security-Policy") != "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(rec.Header())
	}
}

func TestPostAttachesOnlyRoomFiles(t *testing.T) {
	s, roomA, sessionA, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	roomB, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	started, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomB.Room, Name: "worker", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	a := contract.Principal{Kind: contract.PrincipalSession, Session: sessionA}
	b := contract.Principal{Kind: contract.PrincipalSession, Session: started.Session}
	upload := func(caller contract.Principal, room contract.RoomID, body string) contract.Attachment {
		req := httptest.NewRequest("POST", "/v1/files?name=note.txt", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		s.Files(caller, room).ServeHTTP(rec, req)
		var file contract.Attachment
		if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
			t.Fatalf("upload: %d", rec.Code)
		}
		return file
	}
	post := func(caller contract.Principal, room contract.RoomID, file contract.Attachment) *contract.Error {
		_, e := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "file", Attachments: []contract.Attachment{file}})
		return e
	}
	fromA := upload(a, roomA, "a")
	if e := post(b, roomB.Room, fromA); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("other room's upload: %v", e)
	}
	if e := post(b, roomB.Room, upload(b, roomB.Room, "b")); e != nil {
		t.Fatalf("own upload: %v", e)
	}
	if e := post(owner, roomB.Room, fromA); e != nil {
		t.Fatalf("owner: %v", e)
	}
	if e := post(b, roomB.Room, fromA); e != nil {
		t.Fatalf("file posted in the room: %v", e)
	}
}
