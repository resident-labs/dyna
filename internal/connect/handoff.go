package connect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"dyna-agent/internal/websvc"
)

func (s *Service) handoff(w http.ResponseWriter, rt websvc.Route, p websvc.Principal, body map[string]json.RawMessage) {
	if p.Kind != "connect" {
		websvc.JSONError(w, http.StatusBadRequest, "only a Connect session can switch routes")
		return
	}
	if !s.limit.Allow("handoff:"+p.ID, 10, time.Minute) {
		websvc.JSONError(w, http.StatusTooManyRequests, "too many route switches")
		return
	}
	x, ok := s.session(p.ID)
	if !ok {
		websvc.JSONError(w, http.StatusUnauthorized, "this session has ended")
		return
	}
	// A cookie alone must not open a new origin: the source key must sign.
	if _, err := s.checkProof(rt, "/api/connect/handoff", str(body, "proof"), x.JKT); err != nil {
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	target := str(body, "target")
	if target == rt.App || !slices.ContainsFunc(s.o.Routes, func(r Route) bool { return r.App == target }) {
		websvc.JSONError(w, http.StatusBadRequest, "that is not another route of this machine")
		return
	}
	code := randomToken(32)
	s.mu.Lock()
	for k, v := range s.handoffs {
		if s.now().After(v.exp) {
			delete(s.handoffs, k)
		}
	}
	s.handoffs[hashToken(code)] = handoff{session: x.ID, target: target, exp: s.now().Add(HandoffLife)}
	s.mu.Unlock()
	s.o.Store.Audit("session.handoff", fmt.Sprintf("%s to %s", x.ID, target))
	websvc.JSONReply(w, http.StatusOK, Handoff{Code: code})
}

func (s *Service) whoami(w http.ResponseWriter, p websvc.Principal) {
	out := Whoami{Kind: p.Kind, ID: p.ID, Name: p.Name}
	if p.Kind == "connect" {
		if x, ok := s.session(p.ID); ok {
			out.User, out.Expires, out.GrantEnd = &x.UserName, &x.Access, &x.End
		}
	}
	websvc.JSONReply(w, http.StatusOK, out)
}

func (s *Service) routes(w http.ResponseWriter, rt websvc.Route) {
	l, _ := s.link()
	routes := s.o.Routes
	if routes == nil {
		routes = []Route{}
	}
	websvc.JSONReply(w, http.StatusOK, RouteList{Machine: l.Machine, Key: s.PublicKey(), Current: rt.App, Routes: routes})
}
