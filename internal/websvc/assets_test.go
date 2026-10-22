package websvc

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestShellAssetGzip(t *testing.T) {
	e := newEnv(t)
	shell := e.srv.o.Shell.(fstest.MapFS)
	for _, tc := range []struct {
		ext, mime string
		compress  bool
	}{
		{".wasm", "application/wasm", true},
		{".js", "javascript", true},
		{".css", "text/css", true},
		{".svg", "image/svg+xml", true},
		{".woff2", "font/woff2", false},
		{".png", "image/png", false},
		{".jpg", "image/jpeg", false},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			name := "assets/terminal" + tc.ext
			data := bytes.Repeat([]byte("terminal asset bytes\x00"), 4096)
			if tc.ext == ".wasm" {
				data = append([]byte("\x00asm\x01\x00\x00\x00"), data...)
			}
			shell[name] = &fstest.MapFile{Data: data}
			for _, encoding := range []string{"", "br, gzip", "gzip;q=0.5", "gzip;q=0", "*", "*;q=1, gzip;q=0"} {
				t.Run(encoding, func(t *testing.T) {
					wantGzip := tc.compress && (encoding == "br, gzip" || encoding == "gzip;q=0.5" || encoding == "*")
					w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/" + name, header: map[string]string{"Accept-Encoding": encoding}})
					if w.Code != 200 {
						t.Fatalf("status: %d", w.Code)
					}
					if got := w.Header().Get("Content-Type"); !strings.Contains(got, tc.mime) {
						t.Fatalf("content type: %q", got)
					}
					if tc.compress && w.Header().Get("Vary") != "Accept-Encoding" {
						t.Fatalf("vary: %v", w.Header())
					}
					if w.Header().Get("Cache-Control") != "max-age=31536000, immutable" {
						t.Fatalf("cache: %v", w.Header())
					}
					if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
						t.Fatalf("length: %v", w.Header())
					}
					body := w.Body.Bytes()
					if wantGzip {
						if w.Header().Get("Content-Encoding") != "gzip" || len(body) >= len(data) {
							t.Fatalf("gzip headers or size: %v, %d bytes", w.Header(), len(body))
						}
						zr, err := gzip.NewReader(bytes.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						body, err = io.ReadAll(zr)
						if err != nil {
							t.Fatal(err)
						}
						if err := zr.Close(); err != nil {
							t.Fatal(err)
						}
					} else if got := w.Header().Get("Content-Encoding"); got != "" {
						t.Fatalf("unexpected encoding %q", got)
					}
					if !bytes.Equal(body, data) {
						t.Fatal("asset bytes changed")
					}
				})
			}
			head := e.do(e.app, req{method: http.MethodHead, url: tsApp + "/" + name, header: map[string]string{"Accept-Encoding": "gzip"}})
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
				t.Fatalf("HEAD: %d %v, body %d", head.Code, head.Header(), head.Body.Len())
			}
		})
	}
}

func TestPackedCachesResult(t *testing.T) {
	e := newEnv(t)
	original := bytes.Repeat([]byte("original asset\n"), 1000)
	packed, ok := e.srv.packed("asset", original)
	if !ok {
		t.Fatal("compressible asset skipped")
	}
	again, ok := e.srv.packed("asset", []byte("replacement"))
	if !ok || !bytes.Equal(packed, again) {
		t.Fatal("cached asset was recomputed")
	}
	// Cache negative results too, so small or incompressible files do not
	// spend CPU on compression every time a browser asks for them.
	if _, ok := e.srv.packed("small", []byte("a")); ok {
		t.Fatal("larger gzip representation was used")
	}
	if _, ok := e.srv.packed("small", original); ok {
		t.Fatal("negative result was recomputed")
	}
}
