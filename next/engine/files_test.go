package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
	s.SetFilesDir(filepath.Join(dir, "files"))
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	upload := httptest.NewRequest("POST", "/v1/files?name=note.txt", bytes.NewBufferString("hello"))
	upload.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	s.Files(owner, "").ServeHTTP(rec, upload)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatalf("upload: %d", rec.Code)
	}
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES ('a','a',0,1,1),('b','b',0,1,1)")
		return err
	})
	if err != nil {
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
