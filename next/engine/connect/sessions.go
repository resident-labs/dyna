package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type mintCode struct {
	JKT, Origin, Device, User string
	Epoch, Generation         int64
	End, Minted               time.Time
}
type handoffCode struct {
	Session store.ConnectSession
	Target  string
	End     time.Time
}
type grantCacheEntry struct {
	Grant      Grant
	At         time.Time
	Generation int64
}
type rateEntry struct {
	Start time.Time
	Count int
}

// Sessions implements the relay and browser session protocol.
type Sessions struct {
	files     *Files
	store     *store.Store
	now       func() time.Time
	client    *http.Client
	mu        sync.Mutex
	codes     map[string]mintCode
	handoffs  map[string]handoffCode
	grants    map[string]grantCacheEntry
	rates     map[string]rateEntry
	keysTried time.Time
}

func NewSessions(files *Files, state *store.Store, now func() time.Time, client *http.Client) *Sessions {
	return &Sessions{files: files, store: state, now: now, client: client, codes: make(map[string]mintCode), handoffs: make(map[string]handoffCode), grants: make(map[string]grantCacheEntry), rates: make(map[string]rateEntry)}
}

func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return b64.EncodeToString(b[:]), nil
}
func (s *Sessions) link(ctx context.Context) (Link, int64, error) {
	state, err := s.store.ConnectLink(ctx)
	if err != nil || !state.Linked {
		return Link{}, 0, errors.New("not linked")
	}
	l, err := s.files.Link()
	return l, state.Generation, err
}
func (s *Sessions) ManagedOrigin(ctx context.Context) string {
	l, _, err := s.link(ctx)
	if err != nil || routeRank(l.App, l.App) < 0 {
		return ""
	}
	return l.App
}
func (s *Sessions) allow(key string, count int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, v := range s.rates {
		if now.Sub(v.Start) >= time.Minute {
			delete(s.rates, k)
		}
	}
	x := s.rates[key]
	if now.Sub(x.Start) >= time.Minute || now.Before(x.Start) {
		x = rateEntry{Start: now}
	}
	x.Count++
	s.rates[key] = x
	return x.Count <= count
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, struct {
		Error string `json:"error"`
	}{message})
}
func connectCookie(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return "__Host-dyna_connect"
	}
	return "dyna_connect"
}
func setConnectCookie(w http.ResponseWriter, origin, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: connectCookie(origin), Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteStrictMode})
}
func (s *Sessions) Authenticate(ctx context.Context, r *http.Request, origin string) (store.ConnectSession, []contract.Scope, bool, error) {
	c, err := r.Cookie(connectCookie(origin))
	if err != nil || c.Value == "" {
		return store.ConnectSession{}, nil, false, nil
	}
	return s.store.AuthenticateConnect(ctx, sha256.Sum256([]byte(c.Value)), origin, s.now())
}

func (s *Sessions) StreamScopes(ctx context.Context, id string) ([]contract.Scope, bool, error) {
	return s.store.ConnectStreamScopes(ctx, id, s.now())
}

var browserKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var helloNoncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func (s *Sessions) relay(l Link) (*Relay, error) {
	key, err := s.files.existingKey()
	if err != nil {
		return nil, err
	}
	return &Relay{Base: l.Relay, Key: key, Client: s.client, Now: s.now}, nil
}
func (s *Sessions) relayClaims(r *http.Request, op string, l Link, generation int64) (RelayClaims, *Relay, error) {
	if r.Method != http.MethodPost {
		return RelayClaims{}, nil, errors.New("use POST")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 {
		return RelayClaims{}, nil, errors.New("bad relay request")
	}
	var relay *Relay
	keys, err := s.files.Keys(s.now())
	var c RelayClaims
	if err == nil {
		c, err = VerifyRelay(strings.TrimSpace(string(body)), op, l.Relay, l.Machine, keys, s.now())
	}
	if err != nil {
		s.mu.Lock()
		retry := s.keysTried.IsZero() || s.now().Sub(s.keysTried) >= time.Minute
		if retry {
			s.keysTried = s.now()
		}
		s.mu.Unlock()
		if !retry {
			return RelayClaims{}, nil, errors.New("relay keys unavailable")
		}
		relay, err = s.relay(l)
		if err != nil {
			return RelayClaims{}, nil, err
		}
		keys, err = relay.FetchKeys(r.Context())
		if err != nil {
			return RelayClaims{}, nil, err
		}
		if err = s.files.SaveKeys(keys); err != nil {
			return RelayClaims{}, nil, err
		}
		c, err = VerifyRelay(strings.TrimSpace(string(body)), op, l.Relay, l.Machine, keys, s.now())
		if err != nil {
			return c, relay, err
		}
	}
	c.Generation = generation
	err = s.store.ConsumeConnectNonceForLink(r.Context(), c.Epoch, c.Generation, "relay:"+c.Nonce, s.now(), time.Unix(c.Exp, 0).Add(time.Minute))
	return c, relay, err
}
func (s *Sessions) proof(ctx context.Context, token, origin, path, want string, epoch, generation int64) error {
	p, err := verifyProof(token, origin+path, s.now())
	if err != nil {
		return err
	}
	if p.JKT != want {
		return errors.New("proof is from another key")
	}
	return s.store.ConsumeConnectNonceForLink(ctx, epoch, generation, "proof:"+p.JTI, s.now(), s.now().Add(2*time.Minute))
}

// ServePublic handles relay requests and unauthenticated browser exchanges.
func (s *Sessions) ServePublic(w http.ResponseWriter, r *http.Request, origin string, routes []string) bool {
	switch r.URL.Path {
	case "/connect/v1/hello":
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodGet {
			fail(w, 405, "use GET")
			return true
		}
		l, _, err := s.link(r.Context())
		if err != nil {
			fail(w, 404, "not linked")
			return true
		}
		nonce := r.URL.Query().Get("nonce")
		if !helloNoncePattern.MatchString(nonce) || !s.allow("hello", 120) {
			fail(w, 400, "bad nonce")
			return true
		}
		relay, err := s.relay(l)
		if err != nil {
			fail(w, 503, "unavailable")
			return true
		}
		sig := ed25519.Sign(relay.Key, []byte("dyna-connect-hello.v1\n"+l.Machine+"\n"+origin+"\n"+nonce))
		reply(w, 200, struct {
			Machine string `json:"machine"`
			Origin  string `json:"origin"`
			Nonce   string `json:"nonce"`
			Sig     string `json:"sig"`
			Relay   string `json:"relay"`
			Key     string `json:"key"`
		}{l.Machine, origin, nonce, b64.EncodeToString(sig), l.Relay, b64.EncodeToString(relay.Key.Public().(ed25519.PublicKey))})
	case "/connect/v1/mint":
		l, generation, err := s.link(r.Context())
		if err != nil || l.App != origin {
			fail(w, 404, "not found")
			return true
		}
		if !s.allow("mint", 30) {
			fail(w, 429, "too many sign-ins")
			return true
		}
		c, relay, err := s.relayClaims(r, "mint", l, generation)
		if err != nil {
			fail(w, 403, "invalid relay request")
			return true
		}
		if c.Sub != l.Owner.ID || !browserKeyPattern.MatchString(c.JKT) || !strings.HasPrefix(c.Dev, "dev_") || (!slices.Contains(routes, c.Origin) || routeRank(c.Origin, l.App) < 0) {
			fail(w, 403, "invalid mint")
			return true
		}
		code, err := randomSecret()
		if err != nil {
			fail(w, 503, "unavailable")
			return true
		}
		if relay == nil {
			relay, err = s.relay(l)
			if err != nil {
				fail(w, 503, "unavailable")
				return true
			}
		}
		s.mu.Lock()
		for k, v := range s.codes {
			if !s.now().Before(v.End) {
				delete(s.codes, k)
			}
		}
		s.codes[hashToken(code)] = mintCode{c.JKT, c.Origin, c.Dev, c.Sub, c.Epoch, c.Generation, s.now().Add(5 * time.Minute), time.Unix(c.Iat, 0)}
		s.mu.Unlock()
		sig := ed25519.Sign(relay.Key, []byte("dyna-connect-mint.v1\n"+c.Nonce+"\n"+hashToken(code)))
		reply(w, 200, struct {
			Code string `json:"code"`
			Sig  string `json:"sig"`
		}{code, b64.EncodeToString(sig)})
	case "/connect/v1/revoke":
		l, generation, err := s.link(r.Context())
		if err != nil {
			fail(w, 403, "invalid relay request")
			return true
		}
		c, _, err := s.relayClaims(r, "revoke", l, generation)
		if err != nil {
			fail(w, 403, "invalid relay request")
			return true
		}
		s.mu.Lock()
		for k, v := range s.codes {
			if v.Device == c.Dev {
				delete(s.codes, k)
			}
		}
		delete(s.grants, c.Dev)
		if c.Dev != "" {
			err = s.store.RevokeConnectDevice(r.Context(), c.Dev, c.Generation, s.now())
		}
		s.mu.Unlock()
		if err != nil {
			fail(w, 503, "unavailable")
			return true
		}
		reply(w, 200, struct {
			OK bool `json:"ok"`
		}{true})
	case "/api/connect/session":
		s.exchange(w, r, origin, routes)
	default:
		return false
	}
	return true
}

func browserRequest(r *http.Request, origin string) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return r.Method == http.MethodPost && r.Header.Get("Origin") == origin && r.Header.Get("X-Dyna") == "1" && media == "application/json"
}

func (s *Sessions) exchange(w http.ResponseWriter, r *http.Request, origin string, routes []string) {
	if !browserRequest(r, origin) {
		fail(w, 403, "forbidden")
		return
	}
	if !s.allow("session", 20) {
		fail(w, 429, "too many sign-ins")
		return
	}
	var req struct {
		Code    string `json:"code"`
		Handoff string `json:"handoff"`
		Proof   string `json:"proof"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req) != nil {
		fail(w, 400, "bad request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var x store.ConnectSession
	var source string
	if req.Handoff != "" {
		h, ok := s.handoffs[hashToken(req.Handoff)]
		delete(s.handoffs, hashToken(req.Handoff))
		if !ok || !now.Before(h.End) || h.Target != origin || !slices.Contains(routes, origin) || routeRank(origin, "") < 0 {
			fail(w, 403, "invalid handoff")
			return
		}
		p, err := verifyProof(req.Proof, origin+"/api/connect/session", now)
		if err != nil || p.JKT == h.Session.JKT || s.store.ConsumeConnectNonceForLink(r.Context(), h.Session.Epoch, h.Session.Generation, "proof:"+p.JTI, now, now.Add(2*time.Minute)) != nil {
			fail(w, 403, "invalid proof")
			return
		}
		x, source = h.Session, h.Session.ID
		x.Origin, x.JKT = origin, p.JKT
	} else {
		c, ok := s.codes[hashToken(req.Code)]
		delete(s.codes, hashToken(req.Code))
		if !ok || !now.Before(c.End) || c.Origin != origin {
			fail(w, 403, "invalid code")
			return
		}
		if err := s.proof(r.Context(), req.Proof, origin, "/api/connect/session", c.JKT, c.Epoch, c.Generation); err != nil {
			fail(w, 403, "invalid proof")
			return
		}
		x = store.ConnectSession{RelayDevice: c.Device, Origin: origin, JKT: c.JKT, User: c.User, Epoch: c.Epoch, Generation: c.Generation,
			Created: now, Minted: c.Minted, End: now.Add(30 * 24 * time.Hour), Grant: now, Access: now.Add(15 * time.Minute)}
	}
	var secrets [96]byte
	if _, err := rand.Read(secrets[:]); err != nil {
		fail(w, 503, "unavailable")
		return
	}
	token := b64.EncodeToString(secrets[:32])
	x.ID, x.Hash = "cs_"+b64.EncodeToString(secrets[32:64]), sha256.Sum256([]byte(token))
	var err error
	if source != "" {
		err = s.store.HandoffConnectSession(r.Context(), source, x, now)
	} else {
		x.Device = "cd_" + b64.EncodeToString(secrets[64:])
		err = s.store.StartConnectSession(r.Context(), x)
	}
	if err != nil {
		fail(w, 403, "session could not start")
		return
	}
	setConnectCookie(w, origin, token, int(x.End.Sub(now)/time.Second))
	reply(w, 200, struct {
		Session  string    `json:"session"`
		Expires  time.Time `json:"expires"`
		GrantEnd time.Time `json:"grantEnd"`
	}{x.ID, x.Access, x.End})
}
