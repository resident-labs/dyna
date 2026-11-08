package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

var timeZero time.Time

// acceptsGzip reports whether the request takes a gzip body.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") && strings.ReplaceAll(params, " ", "") != "q=0" {
			return true
		}
	}
	return false
}

// compressible names the app files worth compressing: text, not fonts or images, which are compressed already.
func compressible(name string) bool {
	switch path.Ext(name) {
	case ".js", ".css", ".html", ".svg", ".json", ".webmanifest", ".map", ".txt":
		return true
	}
	return false
}

// gzipCache holds each app file's gzip body, made once: the app is embedded, so a file never changes while the engine runs.
type gzipCache struct{ files sync.Map }

func (c *gzipCache) get(name string, data []byte) []byte {
	if body, ok := c.files.Load(name); ok {
		return body.([]byte)
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	body, _ := c.files.LoadOrStore(name, buf.Bytes())
	return body.([]byte)
}

// serveApp writes an app file. A hashed file under assets/ never changes, so the browser keeps it for a year; any other
// file, index.html above all, is checked again on each load so a deploy shows at once.
func (w *Web) serveApp(out http.ResponseWriter, r *http.Request, name string, data []byte) {
	if strings.HasPrefix(name, "assets/") {
		out.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		out.Header().Set("Cache-Control", "no-cache")
	}
	if !compressible(name) {
		http.ServeContent(out, r, name, timeZero, bytes.NewReader(data))
		return
	}
	out.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r) {
		http.ServeContent(out, r, name, timeZero, bytes.NewReader(data))
		return
	}
	body := w.gzipped.get(name, data)
	if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
		out.Header().Set("Content-Type", kind)
	}
	out.Header().Set("Content-Encoding", "gzip")
	http.ServeContent(out, r, name, timeZero, bytes.NewReader(body))
}

// gzipWriter compresses an API response when the client takes gzip. It decides at the first write: a response that is
// an event stream, already encoded, or an image passes through unchanged, so live streams and file downloads keep
// their own framing. Flush flushes the compressed bytes too, so a flushed response still reaches the client at once.
type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

var gzipWriters = sync.Pool{New: func() any { zw, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return zw }}

func (g *gzipWriter) decide() {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	kind := h.Get("Content-Type")
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || strings.HasPrefix(kind, "text/event-stream") || strings.HasPrefix(kind, "image/") || strings.HasPrefix(kind, "video/") || strings.HasPrefix(kind, "application/octet-stream") || strings.HasPrefix(kind, "application/zip") || strings.HasPrefix(kind, "application/gzip") {
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	zw := gzipWriters.Get().(*gzip.Writer)
	zw.Reset(g.ResponseWriter)
	g.zw = zw
}

func (g *gzipWriter) WriteHeader(code int) {
	if code != http.StatusNoContent && code != http.StatusNotModified && code >= 200 {
		g.decide()
	} else {
		g.decided = true
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.decide()
	}
	if g.zw == nil {
		return g.ResponseWriter.Write(p)
	}
	return g.zw.Write(p)
}

func (g *gzipWriter) Flush() {
	g.decide()
	if g.zw != nil {
		_ = g.zw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(g.ResponseWriter).Hijack()
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.zw != nil {
		_ = g.zw.Close()
		g.zw.Reset(nil)
		gzipWriters.Put(g.zw)
		g.zw = nil
	}
}

// withGzip serves the request through a gzipWriter when the client takes gzip. A WebSocket upgrade passes through.
func withGzip(out http.ResponseWriter, r *http.Request, serve func(http.ResponseWriter)) {
	if !acceptsGzip(r) || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		serve(out)
		return
	}
	g := &gzipWriter{ResponseWriter: out}
	defer g.close()
	serve(g)
}
