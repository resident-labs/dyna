package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func gunzip(t *testing.T, body io.Reader) string {
	t.Helper()
	zr, err := gzip.NewReader(body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A hashed asset is kept for a year and sent compressed; index.html is checked on every load; a font is sent as it is.
func TestWebAppFilesAreCachedAndCompressed(t *testing.T) {
	js := strings.Repeat("console.log('dyna');\n", 200)
	app := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Dyna</title>")}, "assets/index-abc.js": {Data: []byte(js)}, "assets/font.woff2": {Data: []byte("wOF2font")}}
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", app, time.Now, &streamDeviceStore{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, gz bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://dyna.localhost:7790"+path, nil)
		if gz {
			r.Header.Set("Accept-Encoding", "gzip, deflate, br")
		}
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, r)
		return rec
	}
	asset := get("/assets/index-abc.js", true)
	if asset.Code != 200 || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || asset.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(asset.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("asset: %d %v", asset.Code, asset.Header())
	}
	if asset.Body.Len() >= len(js) || gunzip(t, asset.Body) != js {
		t.Fatal("asset body is not the gzip of the file")
	}
	if plain := get("/assets/index-abc.js", false); plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != js {
		t.Fatalf("plain asset: %v", plain.Header())
	}
	if page := get("/r/room", true); page.Header().Get("Cache-Control") != "no-cache" || gunzip(t, page.Body) != "<!doctype html><title>Dyna</title>" {
		t.Fatalf("index: %v", page.Header())
	}
	if font := get("/assets/font.woff2", true); font.Header().Get("Content-Encoding") != "" || font.Body.String() != "wOF2font" {
		t.Fatalf("font: %v", font.Header())
	}
}

// An API answer is compressed for a client that takes gzip; an event stream and an image pass through, and a flushed
// stream still reaches the client.
func TestGzipWriterCompressesAnswersAndPassesStreams(t *testing.T) {
	serve := func(kind, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/transcript.read", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		withGzip(rec, r, func(out http.ResponseWriter) {
			out.Header().Set("Content-Type", kind)
			_, _ = io.WriteString(out, body)
			out.(http.Flusher).Flush()
		})
		return rec
	}
	answer := strings.Repeat(`{"entries":[]}`, 100)
	if rec := serve("application/json", answer); rec.Header().Get("Content-Encoding") != "gzip" || gunzip(t, rec.Body) != answer {
		t.Fatalf("json: %v", rec.Header())
	}
	if rec := serve("text/event-stream", "data: x\n\n"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "data: x\n\n" || !rec.Flushed {
		t.Fatalf("stream: %v", rec.Header())
	}
	if rec := serve("image/png", "PNG"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "PNG" {
		t.Fatalf("image: %v", rec.Header())
	}
}
