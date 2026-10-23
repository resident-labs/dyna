package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
)

// --- sessions ---

func (s *Service) endSessions(match func(Session) bool, why string) {
	if err := s.replaceSessions(match, nil, why); err != nil {
		s.logf("connect: end sessions: %v", err)
	}
}

// replaceSessions removes the sessions that match and appends add in one
// store update, so no reader or other writer sees both. It then cleans up
// after the removed sessions.
func (s *Service) replaceSessions(match func(Session) bool, add []Session, why string) error {
	var ended []string
	err := s.o.Store.UpdateSessions(func(list []Session) []Session {
		var out []Session
		for _, x := range list {
			if match(x) {
				ended = append(ended, x.ID)
				continue
			}
			out = append(out, x)
		}
		return append(out, add...)
	})
	if err != nil {
		return err
	}
	if len(ended) > 0 {
		if grants, gerr := devicegrant.Open(filepath.Dir(s.o.Store.Dir())); gerr != nil {
			s.logf("connect: end sessions: %v", gerr)
		} else if gerr = grants.Delete("connect", ended); gerr != nil {
			s.logf("connect: end sessions: %v", gerr)
		}
	}
	for _, id := range ended {
		s.o.Store.Audit("session.end", id+": "+why)
	}
	if len(ended) > 0 && s.o.SessionsEnded != nil {
		s.o.SessionsEnded()
	}
	return nil
}

type exchangeRequest struct {
	Code    string `json:"code"`
	Handoff string `json:"handoff"`
	Proof   string `json:"proof"`
	Name    string `json:"name"`
}

func (s *Service) checkProof(rt websvc.Route, path, proof, wantJKT string) (Proof, error) {
	p, err := verifyProof(proof, rt.App+path, s.now())
	if err != nil {
		return p, err
	}
	if wantJKT != "" && p.JKT != wantJKT {
		return p, errors.New("the proof is from another key")
	}
	if !s.once("proof:"+p.JTI, s.now().Add(2*time.Minute)) {
		return p, errors.New("replayed proof")
	}
	return p, nil
}

// exchange trades a one-time code (from the relay's /open) or a handoff
// code (from another route) plus a key proof for a Connect session.
func (s *Service) exchange(w http.ResponseWriter, r *http.Request, rt websvc.Route) {
	if err := websvc.CheckCSRF(r, rt); err != nil {
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	if !s.limit.Allow("session", 20, time.Minute) {
		websvc.JSONError(w, http.StatusTooManyRequests, "too many sign-ins")
		return
	}
	var req exchangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		websvc.JSONError(w, http.StatusBadRequest, "bad sign-in request")
		return
	}
	l, ok := s.link()
	if !ok {
		websvc.JSONError(w, http.StatusForbidden, "this machine is not linked to Resident Labs Connect")
		return
	}
	now := s.now()
	sess := Session{ID: "cs_" + randomToken(9), Origin: rt.App, Created: now, LastSeen: now, Name: deviceName(r)}
	var from string
	fail := func(msg string) {
		s.o.Store.Audit("session.refused", msg)
		websvc.JSONError(w, http.StatusForbidden, msg)
	}
	switch {
	case req.Code != "":
		s.mu.Lock()
		c, found := s.codes[hashToken(req.Code)]
		delete(s.codes, hashToken(req.Code)) // one use, right or wrong
		s.mu.Unlock()
		if !found || now.After(c.exp) {
			fail("that sign-in code is not valid; sign in again")
			return
		}
		if c.origin != rt.App {
			fail("that sign-in code is for another address")
			return
		}
		p, err := s.checkProof(rt, "/api/connect/session", req.Proof, c.jkt)
		if err != nil {
			fail(err.Error())
			return
		}
		sess.JKT, sess.User, sess.UserName, sess.Device, sess.Epoch = p.JKT, c.user, l.Owner.Name, c.dev, c.epoch
		sess.End, sess.Grant = now.Add(GrantLife), now
	case req.Handoff != "":
		s.mu.Lock()
		h, found := s.handoffs[hashToken(req.Handoff)]
		delete(s.handoffs, hashToken(req.Handoff))
		s.mu.Unlock()
		if !found || now.After(h.exp) || h.target != rt.App {
			fail("that route switch code is not valid")
			return
		}
		src, ok := s.session(h.session)
		if !ok {
			fail("the session you switched from has ended")
			return
		}
		p, err := s.checkProof(rt, "/api/connect/session", req.Proof, "")
		if err != nil {
			fail(err.Error())
			return
		}
		// The new origin gets its own key under the same relay device. The
		// grant end does not move.
		sess.JKT, sess.User, sess.UserName, sess.Device, sess.Epoch = p.JKT, src.User, src.UserName, src.Device, src.Epoch
		sess.End, sess.Grant = src.End, src.Grant
		from = src.ID
	default:
		websvc.JSONError(w, http.StatusBadRequest, "send a code or a handoff")
		return
	}
	if sess.Epoch < l.Epoch {
		fail("the link changed; sign in again")
		return
	}
	tok := randomToken(32)
	sess.Hash = hashToken(tok)
	sess.Access = minTime(now.Add(s.o.AccessLife), sess.End)
	// The mint proves the code came for this machine's owner, so a fresh
	// sign-in is the owner's own login and holds every scope. A handoff
	// copies the moving session's grant; an owner who narrowed a grant by
	// hand narrows it again the same way.
	grantScopes, grantLegacy := scope.All(), false
	if from != "" {
		grants, gerr := devicegrant.Open(filepath.Dir(s.o.Store.Dir()))
		if gerr != nil {
			websvc.JSONError(w, http.StatusInternalServerError, "could not save the session")
			return
		}
		if g, ok, gerr := grants.Get("connect", from); gerr != nil {
			websvc.JSONError(w, http.StatusInternalServerError, "could not save the session")
			return
		} else if ok {
			grantScopes, grantLegacy = g.Scopes, g.Legacy
		}
	}
	if err := s.saveSessionGrant(sess.ID, grantScopes, grantLegacy); err != nil {
		websvc.JSONError(w, http.StatusInternalServerError, "could not save the session")
		return
	}
	// One login, one session: the same browser signing in again (a second
	// mint, a route handoff) ends its older sessions, so stale sessions do
	// not live on for 30 days with a cookie nobody renews. The swap is one
	// store update, so two sign-ins at once cannot end each other.
	if err := s.replaceSessions(func(x Session) bool { return x.Device == sess.Device }, []Session{sess}, "superseded by "+sess.ID); err != nil {
		websvc.JSONError(w, http.StatusInternalServerError, "could not save the session")
		return
	}
	s.o.Store.Audit("session.start", fmt.Sprintf("%s device %s at %s", sess.ID, sess.Device, sess.Origin))
	s.setCookie(w, rt, tok, int(sess.End.Sub(now)/time.Second))
	websvc.JSONReply(w, http.StatusOK, map[string]any{"session": sess.ID, "expires": sess.Access, "grantEnd": sess.End})
}

func (s *Service) saveSessionGrant(id string, scopes []string, legacy bool) error {
	grants, err := devicegrant.Open(filepath.Dir(s.o.Store.Dir()))
	if err != nil {
		return err
	}
	return grants.Put("connect", id, scopes, legacy)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func deviceName(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	osName := "browser"
	for _, k := range [][2]string{{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Mac OS", "Mac"}, {"Windows", "Windows"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, k[0]) {
			osName = k[1]
			break
		}
	}
	for _, k := range [][2]string{{"Firefox/", "Firefox"}, {"Edg/", "Edge"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}} {
		if strings.Contains(ua, k[0]) {
			return k[1] + " on " + osName
		}
	}
	return osName
}

func str(body map[string]json.RawMessage, k string) string {
	var v string
	_ = json.Unmarshal(body[k], &v)
	return v
}
