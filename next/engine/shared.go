package engine

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Shared serves the owner's read-only view of a room's shared folder. All opens,
// including symlink targets, are confined by the OS root rather than string checks.
func (s *Service) Shared(caller contract.Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if caller.Kind != contract.PrincipalOwner {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		room, _, err := s.store.ResolveRoom(r.Context(), r.PathValue("room"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "."
		}
		if !filepath.IsLocal(path) || filepath.Clean(path) != path {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		root, err := os.OpenRoot(s.layout.Shared(room))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		stat, err := root.Stat(path)
		if err != nil || !stat.IsDir() && !stat.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		f, err := root.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tree := strings.HasSuffix(r.URL.Path, "/tree")
		if tree != info.IsDir() {
			http.Error(w, "wrong path kind", http.StatusBadRequest)
			return
		}
		if tree {
			type entry struct {
				Name     string    `json:"name"`
				Dir      bool      `json:"dir"`
				Size     int64     `json:"size"`
				Modified time.Time `json:"modified"`
			}
			entries := []entry{}
			names, err := f.Readdirnames(-1)
			if err != nil {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			for _, name := range names {
				// Stat through the same root follows inner symlinks but hides escaping ones.
				child, err := root.Stat(filepath.Join(path, name))
				if err != nil || !child.IsDir() && !child.Mode().IsRegular() {
					continue
				}
				entries = append(entries, entry{name, child.IsDir(), child.Size(), child.ModTime()})
			}
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].Dir != entries[j].Dir {
					return entries[i].Dir
				}
				return entries[i].Name < entries[j].Name
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Entries []entry `json:"entries"`
			}{entries})
			return
		}
		if !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		// Shared files are untrusted documents, including XHTML and other active types.
		// Match the attachment route's sandbox even when opened as an original.
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		media := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
		if media == "" {
			media = "application/octet-stream"
		}
		w.Header().Set("Content-Type", media)
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}
