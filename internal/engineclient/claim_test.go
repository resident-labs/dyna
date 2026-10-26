package engineclient

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/mode"
)

type claimTransport func(*http.Request) (*http.Response, error)

func (f claimTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEveryCallCarriesTheProcessGuardClaim(t *testing.T) {
	for _, tc := range []struct{ name, mode, session string }{
		{"session", mode.Subagent, "ses_claimed"}, {"root", mode.Root, ""}, {"owner", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(mode.EnvMode, tc.mode)
			t.Setenv(mode.EnvSession, "ses_claimed")
			t.Setenv(mode.EnvRoomID, "room_claimed")
			client := &Client{http: &http.Client{Transport: claimTransport(func(r *http.Request) (*http.Response, error) {
				var claim callspec.Claim
				if err := json.Unmarshal([]byte(r.Header.Get(callspec.ClaimHeader)), &claim); err != nil {
					t.Fatal(err)
				}
				if claim.Session != tc.session || claim.Room != "room_claimed" {
					t.Fatalf("claim = %+v", claim)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("null"))}, nil
			})}}
			if err := client.send(t.Context(), "Health", nil, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
