package connect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func (s *Sessions) checkGrant(ctx context.Context, x store.ConnectSession) (bool, error) {
	l, _, err := s.link(ctx)
	if err != nil {
		return true, nil
	}
	s.mu.Lock()
	cached, ok := s.grants[x.RelayDevice]
	s.mu.Unlock()
	if !ok || cached.Generation != x.Generation || s.now().Sub(cached.At) > time.Minute {
		relay, err := s.relay(l)
		if err != nil {
			return false, err
		}
		grant, err := relay.CheckGrant(ctx, l.Machine, x.User, x.RelayDevice)
		if err != nil {
			var re *RelayError
			if errors.As(err, &re) && (re.Status == http.StatusUnauthorized || re.Status == http.StatusForbidden || re.Status == http.StatusGone) {
				if re.Status == http.StatusGone {
					if e := s.store.UnlinkConnectGeneration(ctx, x.Generation); e != nil {
						return false, e
					}
				}
				return true, nil
			}
			return false, err
		}
		cached = grantCacheEntry{Grant: grant, At: s.now(), Generation: x.Generation}
		if grant.Epoch > x.Epoch {
			if err := s.store.AdvanceConnectEpoch(ctx, grant.Epoch, x.Generation); err != nil {
				return false, err
			}
		}
		s.mu.Lock()
		s.grants[x.RelayDevice] = cached
		s.mu.Unlock()
	}
	return !cached.Grant.Active || x.Epoch < cached.Grant.Epoch, nil
}

// ServePrivate handles proof-bound renewal.
func (s *Sessions) ServePrivate(w http.ResponseWriter, r *http.Request, origin string, x store.ConnectSession, routes []string) {
	if !browserRequest(r, origin) {
		fail(w, 403, "forbidden")
		return
	}
	var req struct {
		Proof  string `json:"proof"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil && r.URL.Path != "/api/connect/logout" && r.URL.Path != "/api/logout" {
		fail(w, 400, "bad request")
		return
	}
	switch r.URL.Path {
	case "/api/connect/routes":
		s.routeList(w, r, origin, routes, x)
	case "/api/connect/handoff":
		s.issueHandoff(w, r, origin, req.Target, req.Proof, x, routes)
	case "/api/connect/renew":
		if err := s.proof(r.Context(), req.Proof, origin, r.URL.Path, x.JKT, x.Epoch, x.Generation); err != nil {
			fail(w, 403, "invalid proof")
			return
		}
		if !s.allow("renew:"+x.ID, 10) {
			fail(w, 429, "too many renewals")
			return
		}
		refused, err := s.checkGrant(r.Context(), x)
		grace := false
		if refused {
			if err := s.store.RevokeConnectDevice(r.Context(), x.RelayDevice, x.Generation, s.now()); err != nil {
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
	case "/api/connect/logout", "/api/logout":
		if !x.CookieStale {
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
