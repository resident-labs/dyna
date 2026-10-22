package websvc

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// shell serves the front end. It is public: a first visit from an outside
// link arrives without the SameSite=Strict cookie, and the page's own API
// calls then decide (a 401 shows the pairing screen). The manifest and icons
// are public too, since a browser may fetch them with no cookie.
func (s *Server) shell(w http.ResponseWriter, r *http.Request, rt Route) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	// /connect is where the relay sends a browser after sign-in.
	if p == "" || p == "index.html" || p == "connect" {
		s.serveIndex(w, rt)
		return
	}
	if strings.HasPrefix(p, "rich/") || strings.HasPrefix(p, ".") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.o.Shell, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		h.Set("Content-Type", t)
	}
	if path.Ext(p) == ".wasm" {
		h.Set("Content-Type", "application/wasm")
	}
	if path.Ext(p) == ".webmanifest" {
		h.Set("Content-Type", "application/manifest+json")
	}
	if strings.HasPrefix(p, "assets/") {
		h.Set("Cache-Control", "max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	switch path.Ext(p) {
	case ".wasm", ".js", ".css", ".svg", ".webmanifest", ".json":
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			if z, ok := s.packed(p, b); ok {
				h.Set("Content-Encoding", "gzip")
				b = z
			}
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

// packed caches the compression result, including when gzip saves no bytes.
// Concurrent requests share one computation. Shell files stay fixed for the
// life of the server, so the file name identifies the cached representation.
func (s *Server) packed(name string, b []byte) ([]byte, bool) {
	compute := sync.OnceValues(func() ([]byte, bool) {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(b)
		if zw.Close() != nil || buf.Len() >= len(b) {
			return nil, false
		}
		return buf.Bytes(), true
	})
	cached, _ := s.gz.LoadOrStore(name, compute)
	return cached.(func() ([]byte, bool))()
}

func (s *Server) serveIndex(w http.ResponseWriter, rt Route) {
	cfg, _ := json.Marshal(ClientConfig{CardOrigin: rt.Cards, Push: s.o.Push != nil})
	connectSrc := "'self'"
	if s.o.Ext != nil {
		var m map[string]any
		_ = json.Unmarshal(cfg, &m)
		for k, v := range s.o.Ext.Meta(rt) {
			m[k] = v
		}
		cfg, _ = json.Marshal(m)
		for _, o := range s.o.Ext.ConnectSrc() {
			if o != rt.App {
				connectSrc += " " + o
			}
		}
	}
	meta := `<meta name="dyna-web" content="` + html.EscapeString(string(cfg)) + `">`
	page := bytes.Replace(s.index, []byte("</head>"), []byte(meta+"\n  </head>"), 1)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// 'wasm-unsafe-eval' lets the terminal compile its WebAssembly. It allows
	// no JavaScript eval, and the wasm still loads only from 'self'.
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; "+
		"img-src 'self' data: blob:; font-src 'self' data:; connect-src "+connectSrc+"; media-src 'self' blob:; "+
		"frame-src "+rt.Cards+"; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; "+
		"frame-ancestors 'none'; manifest-src 'self'")
	_, _ = w.Write(page)
}
