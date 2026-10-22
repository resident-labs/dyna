package websvc

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ComputerHost is what the computer routes need from the computers package.
// desktop wires the real one; tests use a fake. websvc imports no daemon
// code, so it keeps building without labwc or wayvnc. The JSON calls (list,
// status, control) are App methods on the normal Calls table; only the
// picture and the stream are routes, because they are not JSON.
type ComputerHost interface {
	// Shot returns the path of the session's last.png. With fresh, it first asks
	// the daemon for a new picture when labwc runs.
	Shot(session string, fresh bool) (string, error)
	// OpenView opens a viewer lease and returns the RFB connection to wayvnc.
	// key is the page's own name for this viewer. The host maps it to the
	// lease, so a later control call can name the viewer without the page
	// ever seeing the daemon's viewer ID.
	OpenView(session, key string) (ComputerView, error)
}

// ComputerView is one open viewer. Done must be called once, when the
// connection ends. It closes the lease, which also gives back human control.
type ComputerView struct {
	Conn net.Conn
	Done func()
}

var viewKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

var sessionRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

// ValidComputerSession reports whether s can name a session in a computer
// route. The host still checks that the session exists.
func ValidComputerSession(s string) bool { return sessionRef.MatchString(s) }

const (
	// freshEvery is the least time between two fresh screenshot requests for
	// one computer. Faster ones get the picture on disk.
	freshEvery = 5 * time.Second
	// viewPing keeps a quiet stream alive through proxies.
	viewPing = 20 * time.Second
	// maxViewMessage bounds one browser to server RFB message. Input events
	// are tiny; a clipboard paste is the largest.
	maxViewMessage = 1 << 20
)

// computers serves /api/computers/<session>/shot.png and .../view for an
// authenticated request.
func (s *Server) computers(w http.ResponseWriter, r *http.Request, rt Route, tok string, pr Principal) {
	if !pr.Allows(ScopeComputers) {
		jsonError(w, http.StatusForbidden, "this device cannot use computers")
		return
	}
	if s.o.Computers == nil {
		jsonError(w, http.StatusNotFound, "computers are not available")
		return
	}
	session, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/computers/"), "/")
	if !ValidComputerSession(session) {
		jsonError(w, http.StatusBadRequest, "bad session")
		return
	}
	switch action {
	case "shot.png":
		s.shots.Serve(w, r, session)
	case "view":
		s.computerView(w, r, rt, tok, pr, session)
	default:
		jsonError(w, http.StatusNotFound, "no such computer route")
	}
}

// Shots serves a computer's last picture. A fresh picture is asked for at
// most once every 5 s per computer, so a page that reloads its tiles cannot
// make the daemon grab the screen in a loop. The zero value is not usable;
// use NewShots.
type Shots struct {
	host ComputerHost
	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time // session -> last fresh request
}

// NewShots makes a picture server for host.
func NewShots(host ComputerHost) *Shots {
	return &Shots{host: host, now: time.Now, last: map[string]time.Time{}}
}

// freshOK says whether a fresh picture may be asked for now, and records it.
func (p *Shots) freshOK(session string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if t, ok := p.last[session]; ok && now.Sub(t) < freshEvery {
		return false
	}
	if len(p.last) > 256 {
		for k, t := range p.last {
			if now.Sub(t) > time.Minute {
				delete(p.last, k)
			}
		}
	}
	p.last[session] = now
	return true
}

// Serve writes the PNG for one session.
func (p *Shots) Serve(w http.ResponseWriter, r *http.Request, session string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1" && p.freshOK(session)
	path, err := p.host.Shot(session, fresh)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// No picture is not an error: 204 keeps the browser console quiet.
	f, err := os.Open(path)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	// The picture changes, and its address does not.
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "shot.png", st.ModTime(), f)
}

// sameOrigin is the WebSocket form of the CSRF check: a browser always sends
// Origin on a WebSocket handshake, and it must be this route's app origin.
func sameOrigin(r *http.Request, rt Route) bool { return r.Header.Get("Origin") == rt.App }

func (s *Server) computerView(w http.ResponseWriter, r *http.Request, rt Route, tok string, pr Principal, session string) {
	if !sameOrigin(r, rt) {
		jsonError(w, http.StatusForbidden, "the request did not come from the Dyna page")
		return
	}
	key := r.URL.Query().Get("k")
	if !viewKey.MatchString(key) {
		jsonError(w, http.StatusBadRequest, "k must be 8 to 64 letters, digits, - or _")
		return
	}
	ServeComputerView(w, r, s.o.Computers, session, key, func() bool { return s.alive(tok, pr) }, s.o.Logf)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 64 << 10,
	// noVNC asks for "binary". The caller has already checked the Origin.
	Subprotocols: []string{"binary"},
	CheckOrigin:  func(*http.Request) bool { return true },
}

// ServeComputerView upgrades the request to a WebSocket and relays binary RFB
// between it and the computer's wayvnc socket. The caller checks who is
// asking. alive is asked on every ping, so a revoked device ends a long
// stream. The viewer closes when either side ends.
func ServeComputerView(w http.ResponseWriter, r *http.Request, host ComputerHost, session, key string, alive func() bool, logf func(string, ...any)) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	// Open the viewer first: a refusal is then a plain HTTP error the page can read.
	v, err := host.OpenView(session, key)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			_ = v.Conn.Close()
			if v.Done != nil {
				v.Done()
			}
		})
	}
	defer finish()
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has already answered
	}
	defer ws.Close()
	ws.SetReadLimit(maxViewMessage)

	done := make(chan struct{})
	var wmu sync.Mutex
	write := func(typ int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(20 * time.Second))
		return ws.WriteMessage(typ, b)
	}
	// wayvnc to browser.
	go func() {
		defer close(done)
		buf := make([]byte, 64<<10)
		for {
			n, err := v.Conn.Read(buf)
			if n > 0 {
				if werr := write(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && logf != nil {
					logf("web computers: %s: wayvnc read: %v", session, err)
				}
				_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "the stream ended"))
				return
			}
		}
	}()
	// Ping, and recheck the principal.
	go func() {
		t := time.NewTicker(viewPing)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if alive != nil && !alive() {
					_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "signed out"))
					finish()
					_ = ws.Close()
					return
				}
				if write(websocket.PingMessage, nil) != nil {
					finish()
					_ = ws.Close()
					return
				}
			}
		}
	}()
	// Browser to wayvnc. The pong handler needs a read loop to run.
	_ = ws.SetReadDeadline(time.Now().Add(3 * viewPing))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(3 * viewPing)) })
	for {
		typ, b, err := ws.ReadMessage()
		if err != nil {
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * viewPing))
		if typ != websocket.BinaryMessage {
			continue
		}
		if _, err := v.Conn.Write(b); err != nil {
			return
		}
	}
}
