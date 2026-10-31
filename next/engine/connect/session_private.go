package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func (s *Sessions) checkGrant(ctx context.Context, x store.ConnectSession) (bool, bool, error) {
	l, err := s.link(ctx)
	if err != nil {
		return false, true, nil
	}
	s.mu.Lock()
	cached, ok := s.grants[x.RelayDevice]
	s.mu.Unlock()
	if !ok || s.now().Sub(cached.At) > time.Minute {
		relay, err := s.relay(ctx, l)
		if err != nil {
			return false, false, err
		}
		grant, err := relay.CheckGrant(ctx, l.Machine, x.User, x.RelayDevice)
		if err != nil {
			var re *RelayError
			if errors.As(err, &re) && (re.Status == http.StatusForbidden || re.Status == http.StatusUnauthorized || re.Status == http.StatusGone) {
				if re.Status == http.StatusGone {
					if e := s.store.UnlinkConnect(ctx); e != nil {
						return false, false, e
					}
				}
				return false, true, nil
			}
			return false, false, err
		}
		cached = grantCacheEntry{Grant: grant, At: s.now()}
		if grant.Epoch > x.Epoch {
			if err := s.store.AdvanceConnectEpoch(ctx, grant.Epoch); err != nil {
				return false, false, err
			}
		}
		s.mu.Lock()
		s.grants[x.RelayDevice] = cached
		s.mu.Unlock()
	}
	allowed := cached.Grant.Active && x.Epoch >= cached.Grant.Epoch
	return allowed, !allowed, nil
}

// ServePrivate handles proof-bound renewal and handoff.
func (s *Sessions) ServePrivate(w http.ResponseWriter, r *http.Request, origin string, routes []string, x store.ConnectSession) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
		fail(w, 403, "forbidden")
		return
	}
	var req struct {
		Proof  string `json:"proof"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil && r.URL.Path != "/api/connect/whoami" && r.URL.Path != "/api/connect/routes" {
		fail(w, 400, "bad request")
		return
	}
	switch r.URL.Path {
	case "/api/connect/renew":
		if err := s.proof(r.Context(), req.Proof, origin, r.URL.Path, x.JKT, x.Epoch); err != nil {
			fail(w, 403, "invalid proof")
			return
		}
		if !s.allow("renew:"+x.ID, 10) {
			fail(w, 429, "too many renewals")
			return
		}
		ok, refused, err := s.checkGrant(r.Context(), x)
		grace := false
		if refused {
			if err := s.store.RevokeConnectDevice(r.Context(), x.RelayDevice, s.now()); err != nil {
				fail(w, 503, "unavailable")
				return
			}
			setConnectCookie(w, origin, "", -1)
			reply(w, 401, struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}{"session ended", "connect-ended"})
			return
		}
		if err != nil {
			if s.now().Sub(x.Grant) > 15*time.Minute {
				fail(w, 503, "relay unavailable")
				return
			}
			grace = true
		} else if !ok {
			fail(w, 403, "not allowed")
			return
		}
		token, err := randomSecret()
		if err != nil {
			fail(w, 503, "unavailable")
			return
		}
		now := s.now()
		if err = s.store.RotateConnectSession(r.Context(), x, sha256.Sum256([]byte(token)), now, grace); err != nil {
			fail(w, 401, "session ended")
			return
		}
		access := now.Add(15 * time.Minute)
		if x.End.Before(access) {
			access = x.End
		}
		setConnectCookie(w, origin, token, int(x.End.Sub(now)/time.Second))
		reply(w, 200, struct {
			Expires  time.Time `json:"expires"`
			GrantEnd time.Time `json:"grantEnd"`
			Grace    bool      `json:"grace"`
		}{access, x.End, grace})
	case "/api/connect/handoff":
		if x.Stale {
			fail(w, 401, "renew session")
			return
		}
		if err := s.proof(r.Context(), req.Proof, origin, r.URL.Path, x.JKT, x.Epoch); err != nil {
			fail(w, 403, "invalid proof")
			return
		}
		if !s.allow("handoff:"+x.ID, 10) {
			fail(w, 429, "too many handoffs")
			return
		}
		if req.Target == origin || !slices.Contains(routes, req.Target) {
			fail(w, 400, "invalid route")
			return
		}
		code, err := randomSecret()
		if err != nil {
			fail(w, 503, "unavailable")
			return
		}
		s.mu.Lock()
		for k, v := range s.handoffs {
			if !s.now().Before(v.End) {
				delete(s.handoffs, k)
			}
		}
		s.handoffs[hashToken(code)] = handoffCode{x, req.Target, s.now().Add(time.Minute)}
		s.mu.Unlock()
		reply(w, 200, struct {
			Code string `json:"code"`
		}{code})
	case "/api/connect/whoami":
		if x.Stale {
			fail(w, 401, "renew session")
			return
		}
		reply(w, 200, struct {
			Kind     string    `json:"kind"`
			ID       string    `json:"id"`
			Name     string    `json:"name"`
			Expires  time.Time `json:"expires"`
			GrantEnd time.Time `json:"grantEnd"`
		}{"connect", x.ID, x.User, x.Access, x.End})
	case "/api/connect/routes":
		if x.Stale {
			fail(w, 401, "renew session")
			return
		}
		l, err := s.link(r.Context())
		if err != nil {
			fail(w, 403, "not linked")
			return
		}
		relay, err := s.relay(r.Context(), l)
		if err != nil {
			fail(w, 503, "unavailable")
			return
		}
		entries := make([]Route, 0, len(routes))
		for _, route := range routes {
			entries = append(entries, Route{App: route})
		}
		reply(w, 200, struct {
			Machine string  `json:"machine"`
			Key     string  `json:"key"`
			Current string  `json:"current"`
			Routes  []Route `json:"routes"`
		}{l.Machine, b64.EncodeToString(relay.Key.Public().(ed25519.PublicKey)), origin, entries})
	case "/api/connect/logout":
		if !x.Stale {
			if err := s.store.EndConnectSession(r.Context(), x.ID); err != nil {
				fail(w, 503, "unavailable")
				return
			}
		}
		setConnectCookie(w, origin, "", -1)
		reply(w, 200, struct{}{})
	default:
		fail(w, 404, "not found")
	}
}
