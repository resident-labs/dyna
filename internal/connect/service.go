// Package connect is the machine side of Resident Labs Connect: the link
// to a relay account, the managed tunnel, short-lived Connect sessions,
// and signed route hellos. See docs/desktop/CONNECT.md.
//
// Trust. The machine mints every credential that opens it. The relay asks
// with a short signed request (/connect/v1/mint, on the tunnel host only);
// the machine checks it against the relay's cached key set, answers with a
// one-time code bound to the browser's key, and signs the answer with its
// own key. The browser trades the code plus a key proof for a Connect
// session here. The relay never sees a session token.
//
// Sessions. A Connect session is one browser key on one route origin under
// one relay device. Access lasts 15 minutes. Renew needs a fresh proof by
// the same key and a grant check with the relay (cached 60 seconds). The
// cookie value changes on each renew. The previous value works for 30
// seconds (requests in flight); after that it can only renew, which needs
// the key. The grant ends 30 days after the first
// sign-in; renew never passes that. When the relay cannot be reached, renew
// may skip the grant check for Grace (default 15 minutes, at most 1 hour)
// after the last good check; a relay that says no always ends the session.
//
// Connect sessions never replace the pairing code and the device cookie:
// those keep working with no relay at all.
package connect

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/websvc"
)

const (
	AccessLife   = 15 * time.Minute
	GrantLife    = 30 * 24 * time.Hour
	CodeLife     = 5 * time.Minute
	HandoffLife  = time.Minute
	DefaultGrace = 15 * time.Minute
	MaxGrace     = time.Hour
	grantCache   = time.Minute
	prevLife     = 30 * time.Second
	// keySetMaxAge bounds how long a cached relay key set may approve new
	// mints. Live sessions run on regardless.
	keySetMaxAge = 7 * 24 * time.Hour
	keyRefetch   = time.Minute
	seenEvery    = time.Minute
)

// Route is one way to reach this machine, with its kind: loopback,
// tailscale, custom or tunnel.
type Route struct {
	Kind string `json:"kind"`
	websvc.Route
}

// Options configures a Service.
type Options struct {
	Store  *Store
	Routes []Route
	// Grace is how long renew may skip an unreachable relay.
	Grace time.Duration
	// AccessLife is the access window; 0 means AccessLife (15 minutes).
	// Tests shorten it; it may not be longer.
	AccessLife time.Duration
	// SessionsEnded lets the host prune subscriptions after session revocation.
	SessionsEnded func()
	Logf          func(format string, args ...any)
	// HTTP is the client for relay calls; tests set it.
	HTTP *http.Client
	Now  func() time.Time
}

type mintCode struct {
	jkt, origin, dev, user string
	epoch                  int
	exp                    time.Time
}

type handoff struct {
	session, target string
	exp             time.Time
}

type grantEntry struct {
	at     time.Time
	active bool
	epoch  int
}

// Service is the machine's Connect endpoint set. It works without a link:
// then it serves only hello-free routes.
type Service struct {
	o     Options
	key   ed25519.PrivateKey
	now   func() time.Time
	logf  func(string, ...any)
	limit *limiter

	mu        sync.Mutex
	codes     map[string]mintCode
	handoffs  map[string]handoff
	seen      map[string]time.Time
	grants    map[string]grantEntry
	keys      KeySet
	keysTried time.Time
}

// New makes the service. It loads (or makes) the machine key.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("connect: no store")
	}
	if o.Grace == 0 {
		o.Grace = DefaultGrace
	}
	if o.Grace < 0 {
		o.Grace = 0
	}
	if o.Grace > MaxGrace {
		return nil, fmt.Errorf("connect: the relay grace is at most %s", MaxGrace)
	}
	if o.AccessLife == 0 {
		o.AccessLife = AccessLife
	}
	if o.AccessLife < 30*time.Second || o.AccessLife > AccessLife {
		return nil, fmt.Errorf("connect: the access window must be between 30s and %s", AccessLife)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	key, err := o.Store.Key()
	if err != nil {
		return nil, err
	}
	ks, _ := o.Store.KeySet()
	return &Service{
		o: o, key: key, now: o.Now, logf: o.Logf, limit: newLimiter(o.Now),
		codes: map[string]mintCode{}, handoffs: map[string]handoff{}, seen: map[string]time.Time{},
		grants: map[string]grantEntry{}, keys: ks,
	}, nil
}

// PublicKey is the machine key, base64url.
func (s *Service) PublicKey() string { return b64.EncodeToString(s.key.Public().(ed25519.PublicKey)) }

func (s *Service) link() (Link, bool) {
	l, ok, err := s.o.Store.Link()
	if err != nil || !ok || l.Unlinked {
		return Link{}, false
	}
	return l, true
}

func (s *Service) relay(l Link) *Relay {
	return &Relay{Base: l.Relay, Key: s.key, Client: s.o.HTTP}
}

// --- websvc.Extension ---

// Meta goes into the page's config: whether Connect is linked here and the
// pinned identity the page checks. It is public,
// so it holds no route list (the page asks /api/connect/routes for that).
func (s *Service) Meta(rt websvc.Route) map[string]any {
	out := map[string]any{"connect": nil}
	if l, ok := s.link(); ok {
		out["connect"] = map[string]any{"linked": true, "relay": l.Relay, "machine": l.Machine, "key": s.PublicKey(), "name": l.Name}
	}
	return out
}

// ConnectSrc lists the route origins the page may probe with hello.
func (s *Service) ConnectSrc() []string {
	var out []string
	for _, r := range s.o.Routes {
		out = append(out, r.App)
	}
	return out
}

// ServePublic serves the routes that need no session.
func (s *Service) ServePublic(w http.ResponseWriter, r *http.Request, rt websvc.Route) bool {
	switch r.URL.Path {
	case "/connect/v1/hello":
		s.hello(w, r, rt)
	case "/connect/v1/mint":
		s.mint(w, r, rt)
	case "/connect/v1/revoke":
		s.revoke(w, r)
	case "/api/connect/session":
		s.exchange(w, r, rt)
	default:
		return false
	}
	return true
}

// ServePrivate serves the routes that need a session (Connect or paired).
func (s *Service) ServePrivate(w http.ResponseWriter, r *http.Request, rt websvc.Route, p websvc.Principal) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/connect/") {
		return false
	}
	if err := websvc.CheckCSRF(r, rt); err != nil {
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return true
	}
	var body map[string]json.RawMessage
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err == nil && len(b) > 0 && b[0] == '{' {
		err = json.Unmarshal(b, &body)
	}
	if err != nil {
		websvc.JSONError(w, http.StatusBadRequest, "bad request body")
		return true
	}
	switch path {
	case "/api/connect/renew":
		s.renew(w, r, rt, p, body)
	case "/api/connect/handoff":
		s.handoff(w, rt, p, body)
	case "/api/connect/whoami":
		s.whoami(w, p)
	case "/api/connect/routes":
		s.routes(w, rt)
	default:
		websvc.JSONError(w, http.StatusNotFound, "no such route")
	}
	return true
}

// RelayRoutes are the routes the relay may send a browser to: every route
// but plain-HTTP non-loopback ones (there are none; ParseRoute allows
// them only for loopback by convention, and the relay refuses the rest).
func (s *Service) RelayRoutes() []RouteInfo {
	var out []RouteInfo
	for _, r := range s.o.Routes {
		if strings.HasPrefix(r.App, "https://") || r.Kind == "loopback" {
			out = append(out, RouteInfo{Kind: r.Kind, App: r.App, Cards: r.Cards})
		}
	}
	return out
}
