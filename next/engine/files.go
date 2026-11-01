package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var errFileTooLarge = errors.New("file is too large")

func validHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == hash
}

// saveFile stores content in a room under its hash. It renames into place and never writes over a blob, since other rooms may hold links to the same file.
func (s *Service) saveFile(ctx context.Context, room contract.RoomID, content io.Reader, limit int64) (string, int64, error) {
	var f *os.File
	err := s.roomWrite(ctx, room, func() error {
		dir := s.layout.Files(room)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		var err error
		f, err = os.CreateTemp(dir, ".upload-")
		return err
	})
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name())
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(content, limit+1))
	closeErr := f.Close()
	if err != nil {
		return "", 0, err
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	if n > limit {
		return "", 0, errFileTooLarge
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	err = s.roomWrite(ctx, room, func() error { return os.Rename(f.Name(), s.layout.File(room, sum)) })
	return sum, n, err
}

// Files serves room blobs. Every upload belongs to a room: a session's comes from its socket, and the owner names one in the query.
func (s *Service) Files(caller contract.Principal, room contract.RoomID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/files" {
			name := r.URL.Query().Get("name")
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			target := room
			if target == "" {
				target = contract.RoomID(r.URL.Query().Get("room"))
			}
			if name == "" || target == "" || err != nil {
				http.Error(w, "invalid file", http.StatusBadRequest)
				return
			}
			sum, n, err := s.saveFile(r.Context(), target, r.Body, 25<<20)
			switch {
			case errors.Is(err, errFileTooLarge):
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			case errors.Is(err, store.ErrNotFound):
				http.Error(w, "unknown room", http.StatusNotFound)
				return
			case errors.Is(err, store.ErrRoomDeleting):
				http.Error(w, "room is being deleted", http.StatusConflict)
				return
			case err != nil:
				http.Error(w, "unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(contract.Attachment{Name: name, Media: media, Bytes: n, SHA256: sum})
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/files/") {
			http.NotFound(w, r)
			return
		}
		hash := strings.TrimPrefix(r.URL.Path, "/v1/files/")
		if strings.HasPrefix(hash, "visual/") {
			id := strings.TrimPrefix(hash, "visual/")
			seq, err := s.store.VisualPostSeq(r.Context(), contract.EventID(id), room, caller.Kind == contract.PrincipalOwner)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			event, err := s.store.Event(r.Context(), seq)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			post := event.Body.(contract.PostCreated)
			for _, file := range post.Attachments {
				if file.Visual == nil {
					continue
				}
				f, err := os.Open(s.layout.File(event.Room, file.SHA256))
				if err != nil {
					http.NotFound(w, r)
					return
				}
				defer f.Close()
				doc, err := io.ReadAll(f)
				if err != nil {
					http.Error(w, "unavailable", 500)
					return
				}
				info, err := f.Stat()
				if err != nil {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Security-Policy", visual.CSP(file.Visual.AllowNet)+"; frame-ancestors 'self'")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				mode := visual.Default
				switch r.URL.Query().Get("theme") {
				case "light":
					mode = visual.Light
				case "dark":
					mode = visual.Dark
				}
				http.ServeContent(w, r, file.Name, info.ModTime(), bytes.NewReader(visual.ColorScheme(doc, mode)))
				return
			}
			http.NotFound(w, r)
			return
		}
		if !validHash(hash) {
			http.NotFound(w, r)
			return
		}
		media, home, found, err := s.store.FileMedia(r.Context(), hash, room, caller.Kind == contract.PrincipalOwner)
		if !found && err == nil {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", media)
		if media == "text/html" {
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:")
		}
		f, err := os.Open(s.layout.File(home, hash))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, "unavailable", 500)
			return
		}
		http.ServeContent(w, r, hash, info.ModTime(), f)
	})
}
