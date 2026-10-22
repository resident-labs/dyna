package filesvc_test

import (
	"dyna-agent/internal/filesvc"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func workspaceEnv(t *testing.T) (*env, room.Record, string) {
	t.Helper()
	e := newEnv(t)
	root := t.TempDir()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: "workspace", Repo: root, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return e, rec, root
}

func writeWorkspace(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceEntriesAndTrustedPaths(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	for _, dir := range []string{"z-dir", "a-dir", ".git"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkspace(t, root, "z-dir/.git", "gitdir: elsewhere")
	writeWorkspace(t, root, "a.txt", "hello")
	writeWorkspace(t, root, ".env", "visible when requested")
	outside := t.TempDir()
	writeWorkspace(t, outside, "outside.go", "package outside\n")
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := e.files.ListEntries(rec.ID, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries.Entries {
		names = append(names, entry.Name)
		if entry.Name == "link" && entry.Kind != "symlink" {
			t.Fatalf("symlink: %+v", entry)
		}
	}
	if strings.Join(names, ",") != "a-dir,z-dir,a.txt,link" || !entries.Entries[1].IsGit {
		t.Fatalf("entries: %+v", entries)
	}
	hidden, err := e.files.ListEntries(rec.ID, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Entries) != 5 {
		t.Fatalf("hidden: %+v", hidden)
	}
	file, err := e.files.ReadFile(rec.ID, "", "link")
	if err != nil || file.Text != "package outside\n" {
		t.Fatalf("symlink read: %+v %v", file, err)
	}
	file, err = e.files.ReadFile(rec.ID, "", filepath.Join(outside, "outside.go"))
	if err != nil || file.Language != "go" || file.Text != "package outside\n" {
		t.Fatalf("absolute read: %+v %v", file, err)
	}
	// A parent traversal is also intentional under the shell-access trust model.
	relative, err := filepath.Rel(root, filepath.Join(outside, "outside.go"))
	if err != nil {
		t.Fatal(err)
	}
	file, err = e.files.ReadFile(rec.ID, "", relative)
	if err != nil || file.Text != "package outside\n" {
		t.Fatalf("parent read: %+v %v", file, err)
	}
	if _, err := e.files.ReadFile(rec.ID, "", "a-dir"); err == nil {
		t.Fatal("read directory succeeded")
	}
}

func TestWorkspaceSessionRoot(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	writeWorkspace(t, root, "marker.txt", "room")
	session := e.start(rec, "worker", nil)
	writeWorkspace(t, session.Dir, "marker.txt", "session")
	file, err := e.files.ReadFile(rec.ID, session.ID, "marker.txt")
	if err != nil || file.Text != "session" {
		t.Fatalf("session root: %+v %v", file, err)
	}
	other := e.room("other")
	if _, err := e.files.ListEntries(other.ID, session.ID, "", false); err == nil {
		t.Fatal("session from another room accepted")
	}
}

func TestWorkspaceBinaryLargeAndImages(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	writeWorkspace(t, root, "binary.dat", "a\x00b\xff")
	f, err := e.files.ReadFile(rec.ID, "", "binary.dat")
	if err != nil || !f.Binary || f.Text != "" {
		t.Fatalf("binary: %+v %v", f, err)
	}
	// Cut a multibyte rune at the cap. The preview remains valid text.
	large := strings.Repeat("x", filesvc.MaxWorkspaceBytes-1) + "🙂" + strings.Repeat("x", 100)
	writeWorkspace(t, root, "large.txt", large)
	f, err = e.files.ReadFile(rec.ID, "", "large.txt")
	if err != nil || f.Binary || !f.Truncated || len(f.Text) != filesvc.MaxWorkspaceBytes-1 || f.Size != int64(len(large)) {
		t.Fatalf("large: size=%d text=%d binary=%v truncated=%v err=%v", f.Size, len(f.Text), f.Binary, f.Truncated, err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspace(t, root, "image + ?.png", string(png))
	f, err = e.files.ReadFile(rec.ID, "", "image + ?.png")
	if err != nil || !f.Binary || f.URL == "" {
		t.Fatalf("image: %+v %v", f, err)
	}
	handler := e.files.FilesHandler()
	get := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}
	w := get(http.MethodGet, f.URL)
	if w.Code != 200 || w.Body.String() != string(png) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Content-Security-Policy") != fileCSP || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("image route: %d %v", w.Code, w.Header())
	}
	if head := get(http.MethodHead, f.URL); head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %+v", head)
	}
	// An HTML file can only be served as plain text from this route.
	writeWorkspace(t, root, "page.html", "<html><script>alert(1)</script></html>")
	target := "/attachments/" + rec.ID + "/workspace?path=" + url.QueryEscape("page.html")
	if page := get(http.MethodGet, target); page.Header().Get("Content-Type") != "text/plain; charset=utf-8" || page.Header().Get("Content-Security-Policy") != fileCSP {
		t.Fatalf("HTML headers: %v", page.Header())
	}
	if big := get(http.MethodGet, "/attachments/"+rec.ID+"/workspace?path=large.txt"); big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large route: %d", big.Code)
	}
}

const fileCSP = "default-src 'none'; sandbox"
