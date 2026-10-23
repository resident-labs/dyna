package connect

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/websvc"
)

func cookieName(rt websvc.Route) string {
	// __Host- keeps another host on the same site (any account's machine
	// is a residentlabs.dev host) from planting this cookie.
	if strings.HasPrefix(rt.App, "https://") {
		return "__Host-dyna_connect"
	}
	return "dyna_connect"
}

func (s *Service) setCookie(w http.ResponseWriter, rt websvc.Route, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName(rt), Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(rt.App, "https://"),
	})
}

// Authenticate finds the Connect session of a request. It returns
// AuthRenew when the access window ended but the session may renew.
func (s *Service) Authenticate(w http.ResponseWriter, r *http.Request, rt websvc.Route) (websvc.Principal, websvc.AuthResult) {
	c, err := r.Cookie(cookieName(rt))
	if err != nil || c.Value == "" {
		return websvc.Principal{}, websvc.AuthNone
	}
	l, linked := s.link()
	if !linked {
		return websvc.Principal{}, websvc.AuthNone
	}
	h := hashToken(c.Value)
	list, err := s.o.Store.Sessions()
	if err != nil {
		s.logf("connect: sessions: %v", err)
		return websvc.Principal{}, websvc.AuthNone
	}
	now := s.now()
	for _, x := range list {
		old := slices.Index(x.Old, h)
		if x.Hash != h && old < 0 {
			continue
		}
		if x.Origin != rt.App || x.Epoch < l.Epoch {
			return websvc.Principal{}, websvc.AuthNone
		}
		p := websvc.Principal{Kind: "connect", ID: x.ID, Device: x.Device, Name: x.UserName + " · " + x.Name}
		if x.Hash != h && (old > 0 || now.After(x.PrevUntil)) {
			// The value before the last rotation came back. Either the
			// cookie was copied, or the browser lost the new value (Chromium
			// writes cookies to disk lazily, so a browser killed right after
			// a renew comes back with the old one). The old value alone
			// gets nothing: only a renew with the session's key proof.
			if s.limit.Allow("old-cookie:"+x.ID, 1, time.Minute) {
				s.o.Store.Audit("session.old-cookie", x.ID+": an earlier cookie value came back; renew with the key proof required")
			}
			p.Stale = true
			return p, websvc.AuthRenew
		}
		if now.After(x.Access) {
			return p, websvc.AuthRenew
		}
		if now.Sub(x.LastSeen) > seenEvery {
			_ = s.o.Store.UpdateSessions(func(list []Session) []Session {
				for i := range list {
					if list[i].ID == x.ID {
						list[i].LastSeen = now
					}
				}
				return list
			})
		}
		return p, websvc.AuthOK
	}
	return websvc.Principal{}, websvc.AuthNone
}

// Alive rechecks a principal for a long-lived stream.
func (s *Service) Alive(p websvc.Principal) bool {
	x, ok := s.session(p.ID)
	if !ok {
		return false
	}
	l, linked := s.link()
	return linked && x.Epoch >= l.Epoch && s.now().Before(x.Access)
}

func (s *Service) session(id string) (Session, bool) {
	list, err := s.o.Store.Sessions()
	if err != nil {
		return Session{}, false
	}
	for _, x := range list {
		if x.ID == id {
			return x, true
		}
	}
	return Session{}, false
}

// Logout ends this Connect session. An earlier cookie value only clears
// the cookie: whoever holds a copy must not be able to end the session.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request, rt websvc.Route, p websvc.Principal) {
	if !p.Stale {
		s.endSessions(func(x Session) bool { return x.ID == p.ID }, "signed out")
	}
	s.setCookie(w, rt, "", -1)
}
