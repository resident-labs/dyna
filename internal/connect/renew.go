package connect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dyna-agent/internal/websvc"
)

func (s *Service) renew(w http.ResponseWriter, r *http.Request, rt websvc.Route, p websvc.Principal, body map[string]json.RawMessage) {
	if p.Kind != "connect" {
		websvc.JSONError(w, http.StatusBadRequest, "only a Connect session renews")
		return
	}
	x, ok := s.session(p.ID)
	now := s.now()
	if !ok || !now.Before(x.End) {
		s.setCookie(w, rt, "", -1)
		websvc.JSONReply(w, http.StatusUnauthorized, map[string]string{"error": "this session has ended; sign in again", "code": "connect-ended"})
		return
	}
	// The proof comes first, and the limit counts only renewals that pass
	// it, so a cookie thief's bad proofs cannot starve the honest renew.
	if _, err := s.checkProof(rt, "/api/connect/renew", str(body, "proof"), x.JKT); err != nil {
		websvc.JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	if !s.limit.Allow("renew:"+p.ID, 10, time.Minute) {
		websvc.JSONError(w, http.StatusTooManyRequests, "too many renewals")
		return
	}
	ok, refused, err := s.checkGrant(r.Context(), x)
	grace := false
	switch {
	case refused:
		s.endSessions(func(y Session) bool { return y.Device == x.Device }, "relay refused the grant")
		s.setCookie(w, rt, "", -1)
		websvc.JSONReply(w, http.StatusUnauthorized, map[string]string{"error": "your Resident Labs device was revoked or the machine was unlinked; sign in again", "code": "connect-ended"})
		return
	case err != nil && now.Sub(x.Grant) <= s.o.Grace:
		grace = true
		s.o.Store.Audit("session.grace", fmt.Sprintf("%s renewed without the relay (%v)", x.ID, err))
	case err != nil:
		websvc.JSONError(w, http.StatusServiceUnavailable, "the Resident Labs relay cannot be reached, so this session cannot renew; use a paired device or try again")
		return
	case !ok:
		websvc.JSONError(w, http.StatusForbidden, "not allowed")
		return
	}
	tok := randomToken(32)
	err = s.o.Store.UpdateSessions(func(list []Session) []Session {
		for i := range list {
			if list[i].ID == x.ID {
				list[i].Old = append([]string{list[i].Hash}, list[i].Old...)[:min(len(list[i].Old)+1, 3)]
				list[i].PrevUntil = now.Add(prevLife)
				list[i].Hash = hashToken(tok)
				list[i].Access = minTime(now.Add(s.o.AccessLife), list[i].End)
				list[i].LastSeen = now
				if !grace {
					list[i].Grant = now
				}
				x = list[i]
			}
		}
		return list
	})
	if err != nil {
		websvc.JSONError(w, http.StatusInternalServerError, "could not save the session")
		return
	}
	s.setCookie(w, rt, tok, int(x.End.Sub(now)/time.Second))
	websvc.JSONReply(w, http.StatusOK, Renewal{Expires: x.Access, GrantEnd: x.End, Grace: grace})
}
