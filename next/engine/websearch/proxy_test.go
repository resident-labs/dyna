package websearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type proxyTestTransport struct{ target string }

func (t proxyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Host = t.target
	return http.DefaultTransport.RoundTrip(r)
}

func TestProxySearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dyna/search" {
			t.Error("wrong route")
		}
		fmt.Fprint(w, `{"results":[{"title":"Example","url":"https://example.org","snippet":"Answer","provider":"kimi"}]}`)
	}))
	defer server.Close()
	p := ProxySearch{Provider: "kimi", Client: &http.Client{Transport: proxyTestTransport{target: strings.TrimPrefix(server.URL, "http://")}}}
	got, err := p.Search(context.Background(), Query{Text: "query"})
	if err != nil || len(got) != 1 || got[0].Provider != "kimi" || got[0].URL != "https://example.org" {
		t.Fatalf("search: %+v %v", got, err)
	}
}
