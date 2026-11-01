package model

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type searchRoundTrip func(*http.Request) (*http.Response, error)

func (f searchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchTransportEnforcesProxyPolicy(t *testing.T) {
	transport := searchTransport{key: "client-key", eligible: "cHJveHktaWQ", base: searchRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer client-key" || r.Header.Get("X-Dyna-Eligible-Auth") != "cHJveHktaWQ" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Fatal("proxy policy not applied")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "http://cliproxy/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer forged")
	req.Header.Set("X-Dyna-Eligible-Auth", "forged")
	req.Header.Set("ChatGPT-Account-ID", "forged")
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	req.URL.Host = "external"
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("accepted external route")
	}
}
