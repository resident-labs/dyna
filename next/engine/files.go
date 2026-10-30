package engine

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(contract.Attachment{Name: name, Media: media, Bytes: n, SHA256: sum})
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/files/") {
			http.NotFound(w, r)
			return
		}
		hash := strings.TrimPrefix(r.URL.Path, "/v1/files/")
		if !validHash(hash) {
			http.NotFound(w, r)
			return
		}
		query := "SELECT f.media FROM event_files f JOIN events e ON e.seq=f.seq WHERE f.sha256=?"
		args := []string{hash}
		if caller.Kind != contract.PrincipalOwner {
			query += " AND e.room=?"
			args = append(args, string(room))
		}
		query += " ORDER BY f.seq DESC LIMIT 1"
		var media string
		var err error
		if len(args) == 1 {
			err = s.store.Read().QueryRowContext(r.Context(), query, args[0]).Scan(&media)
		} else {
			err = s.store.Read().QueryRowContext(r.Context(), query, args[0], args[1]).Scan(&media)
		}
		if errors.Is(err, sql.ErrNoRows) {
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
