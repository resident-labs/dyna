package roomsvc

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

func TestStoredContentSurvivesSmallerLimits(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rec := e.room("content")
	doc := []byte("<p>keep this stored card</p>")
	a, err := e.svc.Attach(rec.ID, "c.html", doc)
	if err != nil {
		t.Fatal(err)
	}
	a.Render = room.RenderHTML
	if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: "content:lead", To: []string{"content"}, Text: "card", Attachments: []room.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	v := settings.Defaults()
	v.Set(settings.KeyMaxCardBytes, "3")
	v.Set(settings.KeyMaxFileBytes, "4")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Attach(rec.ID, "new", doc); err == nil || !strings.Contains(err.Error(), "maximum 4 bytes") {
		t.Fatalf("new upload: %v", err)
	}
	if b, _, err := e.svc.files().RichDocument(rec.ID, a.ID); err != nil || !bytes.Equal(b, doc) {
		t.Fatalf("stored card: %q %v", b, err)
	}
	for _, route := range []string{"rich", "attachments"} {
		w := httptest.NewRecorder()
		e.svc.files().FilesHandler().ServeHTTP(w, httptest.NewRequest("GET", "/"+route+"/"+rec.ID+"/"+a.ID, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), string(doc)) {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body)
		}
	}
}

func TestBase64UploadUsesDecodedLimit(t *testing.T) {
	e := newEnv(t)
	rec := e.room("base64")
	v := settings.Defaults()
	v.Set(settings.KeyMaxFileBytes, "4")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("1234"))
	if a, err := e.svc.AttachBase64(rec.ID, "f", encoded[:4]+"\r\n"+encoded[4:]); err != nil || a.Size != 4 {
		t.Fatalf("wrapped base64: %+v %v", a, err)
	}
	if _, err := e.svc.AttachBase64(rec.ID, "f", base64.StdEncoding.EncodeToString([]byte("12345"))); err == nil || !strings.Contains(err.Error(), "maximum 4 bytes") {
		t.Fatalf("decoded limit: %v", err)
	}
	if _, err := e.svc.AttachBase64(rec.ID, "f", "!invalid"); err == nil {
		t.Fatal("accepted malformed base64")
	}
}
