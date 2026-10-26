package visual

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests launch a real Chromium per page. A hosted CI runner has two to
// four cores, and ten browsers starting at once starve each other until the
// browser start alone passes Inspect's 15 second budget: that is how they timed
// out. They run one at a time instead, and a test never waits inside its own
// body, so its measured time is the work it does. One inspection takes about
// half a second that way, well inside the per-test budget.
func TestInspect(t *testing.T) {
	fixtures := []struct {
		name, html string
		statuses   map[string]string
	}{
		{"good", `<style>body{color:#000;background:white}</style><h1>Hello</h1><p style="color:oklch(0 0 0);background:oklch(1 0 0)">Modern CSS color</p>`, nil},
		{"visible descendant", `<body style="visibility:hidden"><p style="visibility:visible;color:#777;background:#888">Hello</p></body>`, map[string]string{"contrast": "warn"}},
		{"display contents", `<div style="display:contents">Hello</div>`, nil},
		{"display none", `<body style="display:none"><p>Hello</p></body>`, map[string]string{"renders": "fail"}},
		{"transparent", `<body style="opacity:0"><p>Hello</p></body>`, map[string]string{"renders": "fail"}},
		{"blank", "", map[string]string{"renders": "fail"}},
		{"issues", `<body style="color:#777;background:#888">Direct body text<p>Muted label</p><p style="color:rgba(0,0,0,.1);background:rgba(255,255,255,.5)">Alpha label</p><div style="width:2000px;color:black">Wide</div><img src="https://example.com/a.png"><script>fetch("https://example.com/a.json");console.error("x");throw new Error("broken")</script></body>`, map[string]string{"console": "fail", "widths": "warn", "contrast": "warn", "network": "fail"}},
	}

	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			r := inspectPage(t, []byte(f.html), "", false)
			if len(r.Checks) != 5 {
				t.Fatalf("checks: %+v", r.Checks)
			}
			for i, c := range r.Checks {
				if c.Name != []string{"renders", "console", "widths", "contrast", "network"}[i] {
					t.Errorf("check order: %+v", r.Checks)
				}
				want := f.statuses[c.Name]
				if want == "" {
					want = "pass"
				}
				if c.Status != want {
					t.Errorf("%s: %+v, want %s", c.Name, c, want)
				}
			}
			if f.name == "issues" && !strings.HasPrefix(r.Checks[3].Detail, "3 text elements") {
				t.Errorf("contrast count: %+v", r.Checks[3])
			}
			if f.name == "issues" && !strings.HasPrefix(r.Checks[1].Detail, "2 errors: x") {
				t.Errorf("console count: %+v", r.Checks[1])
			}
			if f.name == "issues" && r.Checks[2].Detail != "Overflow at widths: 360, 768, 1280" {
				t.Errorf("overflow detail: %+v", r.Checks[2])
			}
			if f.name == "good" {
				if len(r.Widths) != 3 {
					t.Fatalf("width screenshots: %d", len(r.Widths))
				}
				for i, shot := range r.Widths {
					want := []int{360, 768, 1280}[i]
					w, h := webpSize(t, shot.Image)
					if shot.Width != want || w != want/2 || h != 400 {
						t.Errorf("width screenshot: width %d, image %dx%d", shot.Width, w, h)
					}
				}
			}
			for _, s := range []struct {
				b    []byte
				w, h int
			}{{r.Banner, 960, 360}, {r.Thumb, 640, 480}} {
				w, h := webpSize(t, s.b)
				if w != s.w || h != s.h {
					t.Errorf("image %dx%d, want %dx%d", w, h, s.w, s.h)
				}
			}
		})
	}
}

func webpSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		t.Fatalf("invalid WebP (%d bytes)", len(b))
	}
	for off := 12; off+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[off+4:]))
		p := b[off+8:]
		switch string(b[off : off+4]) {
		case "VP8X":
			return 1 + int(p[4]) + int(p[5])<<8 + int(p[6])<<16, 1 + int(p[7]) + int(p[8])<<8 + int(p[9])<<16
		case "VP8 ":
			return int(binary.LittleEndian.Uint16(p[6:])) & 0x3fff, int(binary.LittleEndian.Uint16(p[8:])) & 0x3fff
		case "VP8L":
			v := binary.LittleEndian.Uint32(p[1:])
			return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1
		}
		off += 8 + n + (n & 1)
	}
	t.Fatal("no WebP size header")
	return 0, 0
}

func TestInspectCSP(t *testing.T) {
	r := inspectPage(t, []byte(`<p>Hello</p><img src="https://example.com/a.png"><script>console.error("must be blocked")</script>`), "default-src 'none'; style-src 'unsafe-inline'", false)
	if r.Checks[1].Status != "pass" {
		t.Fatalf("CSP did not block script: %+v", r.Checks)
	}
	if r.Checks[4].Status != "fail" {
		t.Fatalf("CSP-blocked external request was not recorded: %+v", r.Checks)
	}
}

func TestInspectBlockedNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(server.Close)
	host, _ := url.Parse(server.URL)
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			r := inspectPage(t, []byte(fmt.Sprintf("<p>Hello</p><img src=%q>", server.URL)), "", allow)
			want := "fail"
			if allow {
				want = "pass"
			}
			if r.Checks[4].Status != want || r.Checks[4].Detail != host.Host {
				t.Fatalf("network check: %+v", r.Checks[4])
			}
			if requests.Load() != 0 {
				t.Fatal("blocked request reached the server")
			}
		})
	}
}

func TestInspectCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Inspect(ctx, []byte("<p>Hello</p>"), "", false)
	if errors.Is(err, ErrNoBrowser) {
		t.Skip("Chromium is not installed")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestInspectBlocksWebSocket(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	address := "wss" + strings.TrimPrefix(server.URL, "https")
	host, _ := url.Parse(address)
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			r := inspectPage(t, []byte(fmt.Sprintf("<p>Hello</p><script>new WebSocket(%q)</script>", address)), "", allow)
			want := "fail"
			if allow {
				want = "pass"
			}
			if r.Checks[4].Status != want || r.Checks[4].Detail != host.Host {
				t.Errorf("WebSocket check: %+v", r.Checks[4])
			}
			if r.Checks[1].Status != "pass" {
				t.Errorf("blocked WebSocket console error: %+v", r.Checks[1])
			}
			if connections.Load() != 0 {
				t.Errorf("WebSocket reached the server: %d connections", connections.Load())
			}
		})
	}
}

func inspectPage(t *testing.T, doc []byte, csp string, allowNet bool) Result {
	t.Helper()
	// Inspect allows itself 15s; the outer deadline stays above it, so a slow
	// browser on a two-core CI runner reports its own error instead of a bare
	// context deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := Inspect(ctx, doc, csp, allowNet)
	if errors.Is(err, ErrNoBrowser) {
		t.Skip("Chromium is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}
