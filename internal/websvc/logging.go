package websvc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
)

// --- logging ---

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// Hijack lets the computer view upgrade to a WebSocket through the logger.
func (s *status) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is not supported")
	}
	return h.Hijack()
}

func (s *status) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *status) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logged logs failures by method and path. It never logs a query string:
// a card ticket lives there.
func (s *Server) logged(name string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &status{ResponseWriter: w, code: 200}
		h.ServeHTTP(sw, r)
		if sw.code >= 400 && sw.code != http.StatusNotFound && !errors.Is(r.Context().Err(), context.Canceled) {
			s.o.Logf("web %s: %s %s -> %d", name, r.Method, r.URL.Path, sw.code)
		}
	})
}
