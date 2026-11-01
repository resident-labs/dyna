package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
)

func (s *Service) SetFilesDir(dir string) { s.filesDir = dir }

func validHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == hash
}

func (s *Service) Files(caller contract.Principal, room contract.RoomID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/files" {
			name := r.URL.Query().Get("name")
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if name == "" || err != nil {
				http.Error(w, "invalid file", http.StatusBadRequest)
				return
			}
			if err = os.MkdirAll(s.filesDir, 0700); err != nil {
				http.Error(w, "unavailable", 500)
				return
			}
			f, err := os.CreateTemp(s.filesDir, ".upload-")
			if err != nil {
				http.Error(w, "unavailable", 500)
				return
			}
			defer os.Remove(f.Name())
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(r.Body, (25<<20)+1))
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				http.Error(w, "unavailable", 500)
				return
			}
			if n > 25<<20 {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			sum := hex.EncodeToString(hash.Sum(nil))
			if err = os.Rename(f.Name(), filepath.Join(s.filesDir, sum)); err != nil {
				http.Error(w, "unavailable", 500)
				return
			}
			if caller.Kind == contract.PrincipalSession {
				if err = s.store.RecordUpload(r.Context(), sum, room); err != nil {
					http.Error(w, "unavailable", 500)
					return
				}
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
				f, err := os.Open(filepath.Join(s.filesDir, file.SHA256))
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
				src, script := "data: blob:", "'unsafe-inline'"
				if file.Visual.AllowNet {
					src, script = "https: data: blob:", "'unsafe-inline' https:"
				}
				csp := "default-src 'none'; script-src " + script + "; connect-src " + src + "; img-src " + src + "; font-src " + src + "; media-src " + src + "; style-src 'unsafe-inline' " + src + "; frame-src 'none'; child-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"
				w.Header().Set("Content-Security-Policy", csp)
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				http.ServeContent(w, r, file.Name, info.ModTime(), bytes.NewReader(visual.ColorScheme(doc)))
				return
			}
			http.NotFound(w, r)
			return
		}
		if !validHash(hash) {
			http.NotFound(w, r)
			return
		}
		media, found, err := s.store.FileMedia(r.Context(), hash, room, caller.Kind == contract.PrincipalOwner)
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
		f, err := os.Open(filepath.Join(s.filesDir, hash))
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
