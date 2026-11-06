package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestSharedFolder(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRoom(context.Background(), "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, nil, time.Now)
	s.SetLayout(box.Layout{Data: data})
	shared := s.layout.Shared("room")
	if err := os.MkdirAll(filepath.Join(shared, "z-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"b.txt": "0123456789", "a.html": "<h1>hello</h1>", "c.svg": "<svg/>", "large.txt": ""} {
		if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// No file-size ceiling: preview reads are bounded by Range, not a 5 MiB gate.
	large, err := os.OpenFile(filepath.Join(shared, "large.txt"), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(6 << 20); err != nil {
		t.Fatal(err)
	}
	large.Close()
	outside := filepath.Join(data, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"inner.txt": "b.txt", "escape.txt": outside, "inner-dir": "z-dir"} {
		if err := os.Symlink(target, filepath.Join(shared, name)); err != nil {
			t.Fatal(err)
		}
	}
	handlerFor := func(caller contract.Principal) http.Handler {
		mux := http.NewServeMux()
		mux.Handle("GET /v1/rooms/{room}/shared/{kind}", s.Shared(caller))
		return mux
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	handler := handlerFor(owner)
	request := func(handler http.Handler, room, kind, path, extra string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/rooms/"+room+"/shared/"+kind+"?path="+url.QueryEscape(path)+extra, nil)
		if extra == "&range" {
			r.Header.Set("Range", "bytes=2-5")
		}
		handler.ServeHTTP(rec, r)
		return rec
	}
	for _, tc := range []struct {
		room, kind, path string
		want             int
	}{
		{"missing", "tree", "", 404}, {"room", "file", "missing", 404}, {"room", "tree", "b.txt", 400}, {"room", "file", "z-dir", 400},
		{"room", "file", "../secret.txt", 400}, {"room", "file", outside, 400}, {"room", "file", "a/../b.txt", 400},
		{"room", "file", "escape.txt", 404}, {"room", "file", "inner.txt", 200}, {"room", "tree", "inner-dir", 200},
	} {
		if rec := request(handler, tc.room, tc.kind, tc.path, ""); rec.Code != tc.want {
			t.Errorf("%+v: %d %s", tc, rec.Code, rec.Body.String())
		}
	}
	rec := request(handler, "room", "tree", "", "")
	var listing struct {
		Entries []struct {
			Name     string
			Dir      bool
			Size     int64
			Modified time.Time
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &listing) != nil {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body.String())
	}
	names := []string{}
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
		if entry.Modified.IsZero() {
			t.Fatal("missing modified")
		}
	}
	if !reflect.DeepEqual(names, []string{"inner-dir", "z-dir", "a.html", "b.txt", "c.svg", "inner.txt", "large.txt"}) {
		t.Fatal(names)
	}
	rec = request(handler, "room", "file", "b.txt", "&range")
	if rec.Code != 206 || rec.Body.String() != "2345" {
		t.Fatalf("range: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"a.html", "c.svg"} {
		rec := request(handler, "room", "file", path, "")
		if rec.Header().Get("Content-Security-Policy") != "sandbox" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal(rec.Header())
		}
	}
	rec = request(handler, "room", "file", "b.txt", "&download=1")
	if rec.Header().Get("Content-Disposition") != "attachment; filename=b.txt" {
		t.Fatal(rec.Header())
	}
	rec = request(handler, "room", "file", "large.txt", "&range")
	if rec.Code != 206 {
		t.Fatalf("large file range: %d", rec.Code)
	}
	rec = request(handlerFor(contract.Principal{Kind: contract.PrincipalSession, Session: "box"}), "room", "tree", "", "")
	if rec.Code != 403 {
		t.Fatalf("box: %d", rec.Code)
	}
}
