package websvc

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/voice/realtime"
)

// VoiceHost is what the voice route needs from the engine's call service.
type VoiceHost interface {
	// Join attaches page to the live call, or fails when no call is live.
	// send hands the call one message from the page; leave, called once
	// when the socket ends, detaches it. The call service ends a call no
	// page uses.
	Join(page *realtime.Page) (send func(realtime.PageMessage), leave func(), err error)
}

// VoiceProtocol is the version of the voice socket's frames. A client may send it as
// ?v=N; a client that sends none speaks version 1. The engine keeps accepting the earlier
// versions, so an installed app works until its owner updates it, and refuses a client
// that speaks a newer one than this engine knows.
const VoiceProtocol = 2

// maxVoiceMessage bounds one page message. Microphone chunks are PCM16
// binary frames in version 2, or base64 PCM in legacy JSON.
const maxVoiceMessage = 4 << 20

// voiceQueue is how many events may wait for a slow page before the call
// drops it, so a stalled socket never holds up the call.
const voiceQueue = 512

// voiceRealtime serves the owner's page of the live call at
// /api/voice/realtime: JSON control/output messages and binary microphone PCM.
func (s *Server) voiceRealtime(w http.ResponseWriter, r *http.Request, rt Route, tok string, pr Principal) {
	if !pr.Allows(ScopeOperate) {
		jsonError(w, http.StatusForbidden, "this device cannot take voice calls")
		return
	}
	if s.o.Voice == nil {
		jsonError(w, http.StatusNotFound, "voice calls are not available")
		return
	}
	if !sameOrigin(r, rt) {
		jsonError(w, http.StatusForbidden, "the request did not come from the Dyna page")
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if v := r.URL.Query().Get("v"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > VoiceProtocol {
			jsonError(w, http.StatusBadRequest, "this Dyna does not speak voice protocol "+v+"; update Dyna")
			return
		}
	}
	p := newVoicePage()
	send, leave, err := s.o.Voice.Join(p.page)
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	defer leave()
	ws, err := voiceUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has already answered
	}
	defer ws.Close()
	p.serve(ws, send, func() bool { return s.alive(tok, pr) })
}

var voiceUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 64 << 10,
	// The caller has already checked the Origin.
	CheckOrigin: func(*http.Request) bool { return true },
}

// voicePage is one page socket on the call. The call's loop hands it
// events through page, which never blocks; one writer sends them.
type voicePage struct {
	page   *realtime.Page
	out    chan []byte
	closed chan struct{} // closed once the call or the route ends the page
	once   sync.Once
	code   int
	reason string
}

func newVoicePage() *voicePage {
	p := &voicePage{out: make(chan []byte, voiceQueue), closed: make(chan struct{})}
	p.page = &realtime.Page{
		Send: func(e realtime.PageEvent) {
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			select {
			case p.out <- b:
			default:
				p.end(websocket.CloseTryAgainLater, "the page is too slow")
			}
		},
		Close: p.end,
	}
	return p
}

// end closes the page with code and reason, once.
func (p *voicePage) end(code int, reason string) {
	p.once.Do(func() {
		p.code, p.reason = code, reason
		close(p.closed)
	})
}

// serve runs the socket until either side ends it. It pings the page and
// rechecks alive on each ping, so a revoked device ends the call's page.
func (p *voicePage) serve(ws *websocket.Conn, send func(realtime.PageMessage), alive func() bool) {
	ws.SetReadLimit(maxVoiceMessage)
	go func() {
		t := time.NewTicker(viewPing)
		defer t.Stop()
		write := func(typ int, b []byte) bool {
			_ = ws.SetWriteDeadline(time.Now().Add(20 * time.Second))
			return ws.WriteMessage(typ, b) == nil
		}
		for {
			select {
			case b := <-p.out:
				if !write(websocket.TextMessage, b) {
					p.end(websocket.CloseGoingAway, "")
				}
			case <-t.C:
				if !alive() {
					p.end(websocket.ClosePolicyViolation, "signed out")
				} else if !write(websocket.PingMessage, nil) {
					p.end(websocket.CloseGoingAway, "")
				}
			case <-p.closed:
				// Send what the call said before it ended the page.
				for len(p.out) > 0 && write(websocket.TextMessage, <-p.out) {
				}
				_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(p.code, p.reason))
				_ = ws.Close()
				return
			}
		}
	}()
	_ = ws.SetReadDeadline(time.Now().Add(3 * viewPing))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(3 * viewPing)) })
	for {
		typ, b, err := ws.ReadMessage()
		if err != nil {
			p.end(websocket.CloseNormalClosure, "")
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * viewPing))
		if typ == websocket.BinaryMessage {
			if len(b)%2 != 0 {
				p.end(websocket.CloseUnsupportedData, "microphone audio must contain whole PCM16 samples")
				return
			}
			if len(b) > 0 {
				send(realtime.PageMessage{Type: "audio", Audio: b})
			}
			continue
		}
		var m realtime.PageMessage
		if typ != websocket.TextMessage || json.Unmarshal(b, &m) != nil {
			continue
		}
		send(m)
	}
}
