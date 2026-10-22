package websvc

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"dyna-agent/internal/callspec"
)

func TestClaimHeaderOnlyAppliesToUnverifiedLocalCallers(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Calls["Claim"] = func(ctx context.Context, _ []json.RawMessage) (any, error) { return callspec.ClaimOf(ctx), nil }
	e.srv.o.CallScopes["Claim"] = ScopeRead
	cookie := e.paired()
	raw := `{"Session":"ses_claimed","Room":"room_claimed"}`
	for _, tc := range []struct {
		name        string
		handler     http.Handler
		cookie, raw string
		status      int
		want        callspec.Claim
	}{
		{"unverified local", asCaller(e.srv.UnixHandler(), identified{caller: Caller{Kind: Unverified}}), "", raw, http.StatusOK, callspec.Claim{Session: "ses_claimed", Room: "room_claimed"}},
		{"verified owner", asCaller(e.srv.UnixHandler(), owner), "", raw, http.StatusOK, callspec.Claim{}},
		{"verified session", asCaller(e.srv.UnixHandler(), identified{caller: Caller{Kind: Session, Session: "ses_verified"}}), "", raw, http.StatusOK, callspec.Claim{}},
		{"verified worker", asCaller(e.srv.UnixHandler(), identified{caller: Caller{Kind: Worker, Run: "wf_verified"}}), "", raw, http.StatusOK, callspec.Claim{}},
		{"paired device", e.app, cookie, raw, http.StatusOK, callspec.Claim{}},
		{"no claim", asCaller(e.srv.UnixHandler(), identified{caller: Caller{Kind: Unverified}}), "", "", http.StatusOK, callspec.Claim{}},
		{"bad unverified claim", asCaller(e.srv.UnixHandler(), identified{caller: Caller{Kind: Unverified}}), "", "{", http.StatusBadRequest, callspec.Claim{}},
		{"bad verified claim ignored", asCaller(e.srv.UnixHandler(), owner), "", "{", http.StatusOK, callspec.Claim{}},
		{"bad paired claim ignored", e.app, cookie, "{", http.StatusOK, callspec.Claim{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := "http://engine/api/Claim"
			if tc.cookie != "" {
				target = tsApp + "/api/Claim"
			}
			response := e.do(tc.handler, req{url: target, cookie: tc.cookie, body: "[]", header: map[string]string{callspec.ClaimHeader: tc.raw}})
			if response.Code != tc.status {
				t.Fatalf("response = %d %s", response.Code, response.Body)
			}
			if tc.status != http.StatusOK {
				return
			}
			var got callspec.Claim
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got != tc.want {
				t.Fatalf("claim = %+v, %v", got, err)
			}
		})
	}
}
