package git

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// readHeaders are the only request headers a box may send on: what makes a cheap conditional read. Above all
// this drops Authorization, so the box cannot choose the identity, and Cookie.
var readHeaders = []string{"Accept", "If-None-Match", "If-Modified-Since", "Range", "User-Agent", "X-GitHub-Api-Version"}

// ReadOnly lets a Run read GitHub's REST API with the engine's token, which never enters the box. Only GET and
// HEAD pass, so a Run cannot write whatever the token could. GraphQL is refused whatever the method: its queries
// are POSTs, and telling a query from a mutation would mean parsing the document exactly as GitHub does.
//
// A redirect goes back to the box unfollowed, with the token still on this side.
func ReadOnly(upstream *url.URL, transport http.RoundTripper, token string) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host, r.Out.Host = upstream.Scheme, upstream.Host, upstream.Host
			r.Out.URL.Path, r.Out.URL.RawPath = r.In.URL.Path, r.In.URL.RawPath
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Header = http.Header{}
			for _, name := range readHeaders {
				if values := r.In.Header.Values(name); len(values) > 0 {
					r.Out.Header[http.CanonicalHeaderKey(name)] = values
				}
			}
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead || strings.HasPrefix(strings.ToLower(r.URL.Path), "/graphql") {
			http.Error(w, "a Run reads GitHub with GET and HEAD only; GraphQL is not available", http.StatusForbidden)
			return
		}
		if token == "" {
			http.Error(w, "GH_TOKEN is not set", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
