package filesvc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestFilesHandlerServesCardsOnlyFromRichRecords(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	doc := []byte(`<!doctype html><title>Card</title><p style="color:red">hi</p>`)
	a, err := e.rooms.PutAttachment(rec.ID, "card.html", doc)
	if err != nil {
		t.Fatal(err)
	}
	h := e.svc.FilesHandler()

	// Attached plain, the same bytes are text and never a card.
	if _, err := e.rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "source", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	if w := get(t, h, "/rich/"+rec.ID+"/"+a.ID); w.Code != http.StatusNotFound {
		t.Fatalf("a plain .html attachment must not serve as a card: %d", w.Code)
	}
	w := get(t, h, "/attachments/"+rec.ID+"/"+a.ID)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Content-Security-Policy") != fileCSP {
		t.Fatalf("attachments must never serve HTML: %d %v", w.Code, w.Header())
	}

	a.Render = room.RenderHTML
	if _, err := e.rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "card", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	w = get(t, h, "/rich/"+rec.ID+"/"+a.ID)
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Body.String() != string(doc)+cardAutoHeight {
		t.Fatalf("card = %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	// Scripts run in an opaque-origin sandbox; with no --allow-net there is
	// no network at all.
	for _, want := range []string{"default-src 'none'", "script-src 'unsafe-inline';", "connect-src data: blob:;", "img-src data: blob:;",
		"frame-src 'none'", "child-src 'none'", "object-src 'none'", "worker-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("card CSP lacks %q: %s", want, csp)
		}
	}
	if !strings.HasSuffix(csp, "; sandbox allow-scripts") || strings.Contains(csp, "http") || strings.Contains(csp, "allow-same-origin") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("card headers = %v", w.Header())
	}

	img, _ := e.rooms.PutAttachment(rec.ID, "shot.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0})
	if w := get(t, h, "/attachments/"+rec.ID+"/"+img.ID); w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("images keep their type: %v", w.Header())
	}
	for _, p := range []string{"/rich/" + rec.ID + "/" + img.ID, "/rich/" + rec.ID + "/../x", "/rich/x/" + a.ID, "/attachments/" + rec.ID, "/other/" + rec.ID + "/" + a.ID} {
		if w := get(t, h, p); w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s = %d %q, want an uncached 404", p, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/rich/"+rec.ID+"/"+a.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST = %d", w.Code)
	}
}

func TestFilesHandlerForNamesTheAppOrigins(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	a, err := e.rooms.PutAttachment(rec.ID, "card.html", []byte(`<!doctype html><p>hi</p>`))
	if err != nil {
		t.Fatal(err)
	}
	a.Render = room.RenderHTML
	if _, err := e.rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "card", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	h := e.svc.FilesHandlerFor("http://127.0.0.1:7457 https://dyna.example.ts.net")
	w := get(t, h, "/rich/"+rec.ID+"/"+a.ID)
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") != room.CardCSP(false, "http://127.0.0.1:7457 https://dyna.example.ts.net") {
		t.Fatalf("card CSP = %d %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	if w := get(t, e.svc.FilesHandler(), "/rich/"+rec.ID+"/"+a.ID); w.Header().Get("Content-Security-Policy") != room.CardCSP(false, "'self'") {
		t.Fatal("the desktop CSP must keep frame-ancestors 'self'")
	}
	// The newest post that shows the card decides: --allow-net opens https.
	a.Net = true
	if _, err := e.rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "card with network", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	if w := get(t, h, "/rich/"+rec.ID+"/"+a.ID); w.Header().Get("Content-Security-Policy") != room.CardCSP(true, "http://127.0.0.1:7457 https://dyna.example.ts.net") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("allowed card = %v", w.Header())
	}
	// And back: the same bytes posted without --allow-net close the network again.
	a.Net = false
	if _, err := e.rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "card without network", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	if w := get(t, h, "/rich/"+rec.ID+"/"+a.ID); w.Header().Get("Content-Security-Policy") != room.CardCSP(false, "http://127.0.0.1:7457 https://dyna.example.ts.net") {
		t.Fatalf("closed again CSP = %q", w.Header().Get("Content-Security-Policy"))
	}
}
