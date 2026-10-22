// Package websvc serves the Dyna desktop front end to a browser: the same
// Svelte build, the same Go API, over HTTP. dyna engine run mounts it;
// it has no Wails or cgo code.
//
// Exposure. Both listeners bind loopback only. A phone reaches them through
// a proxy the owner runs: tailscale serve (tailnet HTTPS) or a cloudflared
// named tunnel on the owner's domain. The transport proves nothing. A
// one-time pairing code, minted at the local terminal, and then a device
// cookie are the only proof for browser requests. The private Unix listener
// instead trusts the kernel permissions on its socket directory.
//
// One owner. A paired device acts for the human room owner. Its granted
// scopes limit which calls it may use. The engine identifies a caller on the
// local Unix socket from its process: the owner, a session or a workflow
// worker. A refused caller is stopped before any call runs. Every event stream a reader may open gets every room's events.
// That is right for one owner and wrong for any multi-user mode.
//
// Two origins. The app origin serves the shell, the API, the event stream
// and attachments. A second listener, the card origin, serves only HTML
// cards (/rich/), so a card frame is cross-origin to the app even before its
// sandbox. Cards there need a short ticket, not a cookie.
package websvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/terminalsvc"

	"dyna-agent/internal/websvc/devicestore"
)

const (
	maxPairBody = 4 << 10
	heartbeat   = 15 * time.Second
	// pollWait is how long a long poll waits for an event before it answers
	// with none. It stays well under common proxy idle limits.
	pollWait = 25 * time.Second
)

// ClientConfig supplies the card origin and push availability in a meta tag.
// The extension adds its own Connect fields.
type ClientConfig struct {
	CardOrigin string `json:"cardOrigin"`
	Push       bool   `json:"push"`
}

// Principal is who a request acts for: a paired device or a Connect
// session, or a caller on the private Unix socket.
type Principal struct {
	Kind string // "device", "connect" or "local"
	ID   string // device ID or Connect session ID
	// Device is the device that owns push subscriptions: the paired device,
	// or the relay device behind a Connect session.
	Device string
	Name   string
	// Stale marks a request with an earlier Connect cookie value: it may
	// only renew, and its logout clears the cookie without ending the
	// session.
	Stale bool
	// Scopes is the grant loaded for this request. A local caller ignores it.
	// A missing grant is an empty list, which allows nothing.
	Scopes []string
	// Caller is who a local caller acts for, resolved from its process.
	Caller Caller
}

// Caller is who a local Unix socket caller acts for, as the engine
// resolved it from the calling process.
type Caller struct {
	Kind    CallerKind
	Session string // for a session
	Run     string // for a workflow worker
	Agent   int    // the run's agent, or 0 for the run itself
}

// Allows reports whether this caller may use one scope. The local Unix
// socket may use every scope. Any other caller needs the scope in Scopes.
func (p Principal) Allows(s Scope) bool {
	if p.Kind == "local" {
		return true
	}
	if !ValidScope(s) {
		return false
	}
	want := string(s)
	for _, have := range p.Scopes {
		if have == want {
			return true
		}
	}
	return false
}

type principalContextKey struct{}
type localContextKey struct{}
type callerContextKey struct{}

// CallerKind is what a local caller acts for.
type CallerKind string

const (
	Owner   CallerKind = "owner"
	Session CallerKind = "session"
	Worker  CallerKind = "worker"
	// Unverified is a caller the identity rules would refuse, while the
	// engine only reports refusals (item 3's first release). It is never the
	// owner: code that acts on a caller must handle it on its own.
	Unverified CallerKind = "unverified"
)

// identified is a connection's caller, or why the engine refused it.
type identified struct {
	caller  Caller
	refused error
}

// CallerFromContext returns who a local Unix socket caller acts for.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	id, ok := ctx.Value(callerContextKey{}).(identified)
	return id.caller, ok && id.refused == nil
}

// UnixConnContext identifies each connection on the private Unix socket
// once, right after accept, so a reused PID cannot change who it is. Set it
// as the Unix http.Server's ConnContext.
func (s *Server) UnixConnContext(ctx context.Context, c net.Conn) context.Context {
	id := identified{refused: errors.New("the engine cannot identify callers")}
	if s.o.Callers != nil {
		id.caller, id.refused = s.o.Callers(c)
	}
	return context.WithValue(ctx, callerContextKey{}, id)
}

// PrincipalFromContext returns the authenticated caller, if the context has one.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

// PrincipalResult is an API result that varies with the authenticated caller.
// ForPrincipal must return a separate value so concurrent calls cannot share
// caller state. Desktop bindings return the original, unpersonalized value.
type PrincipalResult interface {
	ForPrincipal(Principal) any
}

// AuthResult is an extension's answer about a request.
type AuthResult int

const (
	AuthNone  AuthResult = iota // no session here
	AuthOK                      // a live session
	AuthRenew                   // a session whose short access window ended; it may renew
)

// Extension adds a second way to sign in (Resident Labs Connect) and its
// routes. websvc stays free of relay code; internal/connect implements it.
type Extension interface {
	// Meta adds fields to the page config. The page is public.
	Meta(rt Route) map[string]any
	// ConnectSrc lists extra origins the page may fetch (route hellos).
	ConnectSrc() []string
	// ServePublic serves routes that need no session; true when it did.
	ServePublic(w http.ResponseWriter, r *http.Request, rt Route) bool
	// Authenticate finds a session for a request with no device cookie.
	Authenticate(w http.ResponseWriter, r *http.Request, rt Route) (Principal, AuthResult)
	// Alive rechecks a principal on each event stream heartbeat.
	Alive(p Principal) bool
	// ServePrivate serves the extension's routes for a signed-in request.
	ServePrivate(w http.ResponseWriter, r *http.Request, rt Route, p Principal) bool
	// Logout ends the principal's session.
	Logout(w http.ResponseWriter, r *http.Request, rt Route, p Principal)
}

// Options configures a Server.
type Options struct {
	Routes []Route
	Store  *devicestore.Store
	// Shell is the built front end (index.html, assets/, manifest, icons).
	Shell fs.FS
	// Calls are the API methods the web may use, by name.
	Calls map[string]Call
	// Files serves /attachments/<room>/<id>; Cards serves /rich/<room>/<id>
	// with frame-ancestors set to FrameAncestors(Routes).
	Files, Cards http.Handler
	Hub          *hub.Hub
	// Terminals streams PTY output through the same owner auth and CSRF checks.
	Terminals *terminalsvc.Service
	// Computers serves /api/computers/<session>/shot.png and /view: agent
	// desktops. nil turns the routes off.
	Computers ComputerHost
	// Voice relays /api/voice/realtime to the owner's live voice call. nil
	// turns the route off.
	Voice VoiceHost
	// Ext is Resident Labs Connect, or nil.
	Ext Extension
	// Push serves browser notifications independently of Connect.
	Push *Push
	// Callers identifies a caller on the private Unix socket from its
	// connection. An error refuses it, and its text is the reply. Nil refuses
	// every local caller.
	Callers func(net.Conn) (Caller, error)
	// CallScopes is the scope of each bound call. A remote caller cannot use
	// a name that is missing here. The local Unix socket does not consult it.
	CallScopes map[string]Scope
	Logf       func(format string, args ...any)
	// PairDelay slows every pairing try. Default 500 ms.
	PairDelay time.Duration
	// Version is the engine's build, sent back on every call
	// (callspec.VersionHeader). Empty sends none.
	Version string
}

// Server holds both handlers.
type Server struct {
	o       Options
	tickets Tickets
	index   []byte
	byApp   map[string]Route // by Host header
	byCard  map[string]bool
	gz      sync.Map // shell file name -> func() ([]byte, bool), computed once
	wires   wireCache

	shots *Shots // nil without Options.Computers
}

// New checks the options and loads the shell.
func New(o Options) (*Server, error) {
	if len(o.Routes) == 0 || o.Store == nil || o.Shell == nil || o.Files == nil || o.Cards == nil || o.Hub == nil {
		return nil, errors.New("websvc: incomplete options")
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.PairDelay == 0 {
		o.PairDelay = 500 * time.Millisecond
	}
	key, err := o.Store.Key()
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(o.Shell, "index.html")
	if err != nil {
		return nil, fmt.Errorf("websvc: the front end is not built: %w", err)
	}
	if !bytes.Contains(index, []byte("</head>")) {
		return nil, errors.New("websvc: index.html has no </head>")
	}
	var shots *Shots
	if o.Computers != nil {
		shots = NewShots(o.Computers)
	}
	s := &Server{shots: shots, o: o, tickets: Tickets{key: key, now: o.Store.Now}, index: index, byApp: map[string]Route{}, byCard: map[string]bool{}}
	for _, r := range o.Routes {
		s.byApp[hostOf(r.App)] = r
		s.byCard[hostOf(r.Cards)] = true
	}
	return s, nil
}

// --- shared checks ---

func (s *Server) common(w http.ResponseWriter, r *http.Request, known bool) bool {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// tailscale serve marks Funnel (public internet) traffic. Dyna never
	// answers it, so turning Funnel on by mistake exposes nothing.
	if r.Header.Get("Tailscale-Funnel-Request") != "" {
		http.Error(w, "dyna does not answer Tailscale Funnel requests", http.StatusForbidden)
		return false
	}
	// The Host allowlist stops DNS rebinding and shows a proxy that rewrites
	// Host. X-Forwarded-Host is never read.
	if !known {
		http.Error(w, fmt.Sprintf("dyna does not serve the host %q; add it with --route", r.Host), http.StatusMisdirectedRequest)
		return false
	}
	return true
}

// --- app origin ---

// UnixHandler serves the same app API to callers authenticated by the Unix
// socket directory permissions. Mount it only on a private Unix listener.
func (s *Server) UnixHandler() http.Handler {
	handler := http.NewServeMux()
	handler.HandleFunc("GET /debug/pprof/", pprof.Index)
	handler.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	handler.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	handler.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	handler.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	handler.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	handler.Handle("/", s.AppHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A refused caller is stopped before any call runs (item 26).
		id, ok := r.Context().Value(callerContextKey{}).(identified)
		if !ok {
			id.refused = errors.New("the engine did not identify this connection")
		}
		if id.refused != nil {
			jsonError(w, http.StatusForbidden, "the engine refused this call: "+id.refused.Error())
			return
		}
		if id.caller.Kind == "" || id.caller.Kind == Unverified {
			jsonError(w, http.StatusForbidden, "the engine refused this call: caller is unverified")
			return
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), localContextKey{}, true)))
	})
}

func localRequest(r *http.Request) bool {
	local, _ := r.Context().Value(localContextKey{}).(bool)
	return local
}

// AppHandler serves the app origin.
func (s *Server) AppHandler() http.Handler {
	return s.logged("app", http.HandlerFunc(s.serveApp))
}

func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	rt, known := s.byApp[strings.ToLower(r.Host)]
	if localRequest(r) {
		rt, known = s.o.Routes[0], true
	}
	if !s.common(w, r, known) {
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
	p := r.URL.Path
	// Profiles can contain credentials and are available only on the private socket.
	if p == "/debug/pprof" || strings.HasPrefix(p, "/debug/pprof/") {
		http.NotFound(w, r)
		return
	}
	if s.o.Ext != nil && s.o.Ext.ServePublic(w, r, rt) {
		return
	}
	switch {
	case p == "/api/pair":
		s.pair(w, r, rt)
		return
	case p == "/api/android/app.apk", p == "/api/android/version":
		s.android(w, r)
		return
	case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/attachments/"):
	default:
		s.shell(w, r, rt)
		return
	}
	// Everything below needs a paired device, a Connect session, or the Unix socket.
	tok := token(r)
	pr, res := Principal{}, AuthNone
	if localRequest(r) {
		caller, _ := CallerFromContext(r.Context())
		pr, res = Principal{Kind: "local", Name: "cli", Caller: caller}, AuthOK
	} else if dev, ok := s.o.Store.Check(tok); ok {
		pr, res = Principal{Kind: "device", ID: dev.ID, Device: dev.ID, Name: dev.Name}, AuthOK
		// The device's expiry just slid, or is about to: send a fresh
		// cookie so the browser's copy lives as long as the device does.
		if dev.RenewDue(s.o.Store.Now()) {
			http.SetCookie(w, s.cookie(rt, tok, int(devicestore.DeviceLife/time.Second)))
		}
	} else if s.o.Ext != nil {
		pr, res = s.o.Ext.Authenticate(w, r, rt)
	}
	switch {
	case res == AuthNone:
		jsonError(w, http.StatusUnauthorized, "pair this device first")
		return
	case res == AuthRenew && p != "/api/connect/renew" && p != "/api/logout":
		jsonReply(w, http.StatusUnauthorized, map[string]string{"error": "this session needs to renew", "code": "renew"})
		return
	}
	if err := s.loadGrant(&pr); err != nil {
		s.o.Logf("web: device grant: %v", err)
		jsonError(w, http.StatusInternalServerError, "device grant could not be read")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, pr))
	switch {
	case strings.HasPrefix(p, "/attachments/"):
		if !pr.Allows(ScopeRead) {
			jsonError(w, http.StatusForbidden, "this device cannot read attachments")
			return
		}
		s.o.Files.ServeHTTP(w, r)
	case p == "/api/terminal/attach":
		s.terminalAttach(w, r, rt, tok, pr)
	case strings.HasPrefix(p, "/api/computers/"):
		s.computers(w, r, rt, tok, pr)
	case p == "/api/voice/realtime":
		s.voiceRealtime(w, r, rt, tok, pr)
	case p == "/api/events":
		s.events(w, r, tok, pr)
	case p == "/api/events/poll":
		s.poll(w, r, pr)
	case p == "/api/logout":
		if err := csrf(r, rt); err != nil {
			jsonError(w, http.StatusForbidden, err.Error())
			return
		}
		if pr.Kind == "connect" {
			s.o.Ext.Logout(w, r, rt, pr)
		} else {
			s.o.Store.Forget(tok)
			http.SetCookie(w, s.cookie(rt, "", -1))
		}
		jsonReply(w, http.StatusOK, map[string]bool{"ok": true})
	case strings.HasPrefix(p, "/api/push/"):
		s.o.Push.serve(w, r, rt, pr)
	case s.o.Ext != nil && s.o.Ext.ServePrivate(w, r, rt, pr):
	default:
		s.call(w, r, rt, pr, strings.TrimPrefix(p, "/api/"))
	}
}

// loadGrant fills Scopes from device_grants for a remote caller. It ignores
// any scopes the authenticator put on the principal. A missing row allows
// nothing. The local computer browser is the exception: its first request
// records every scope. The local Unix socket is not looked up.
func (s *Server) loadGrant(pr *Principal) error {
	if pr.Kind == "local" {
		return nil
	}
	kind := ""
	switch pr.Kind {
	case "device":
		kind = "paired"
	case "connect":
		kind = "connect"
	default:
		pr.Scopes = nil
		return nil
	}
	if pr.ID == "" {
		pr.Scopes = nil
		return nil
	}
	grants, err := devicegrant.Open(filepath.Dir(s.o.Store.Dir()))
	if err != nil {
		return err
	}
	g, ok, err := grants.Get(kind, pr.ID)
	if err != nil {
		return err
	}
	if !ok {
		if kind == "paired" {
			if err := s.ensureComputerBrowser(pr.ID); err != nil {
				return err
			}
			g, ok, err = grants.Get(kind, pr.ID)
			if err != nil {
				return err
			}
		}
		if !ok {
			pr.Scopes = nil
			return nil
		}
	}
	pr.Scopes = g.Scopes
	return nil
}

// alive rechecks the request's principal for a long-lived stream.
func (s *Server) alive(tok string, p Principal) bool {
	if p.Kind == "local" {
		return true
	}
	if p.Kind == "connect" {
		return s.o.Ext.Alive(p)
	}
	return s.o.Store.Alive(tok)
}
