package websvc

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Route is one way to reach the server: the app origin a browser opens and
// the card origin that serves its cards. Both are origins: scheme, host and
// a port only when it is not the scheme's default.
type Route struct {
	App   string `json:"app"`
	Cards string `json:"cards"`
}

// ParseRoute reads "<app origin>,<card origin>", for example
// "https://dyna.example.ts.net,https://dyna.example.ts.net:8443".
func ParseRoute(spec string) (Route, error) {
	a, c, ok := strings.Cut(spec, ",")
	if !ok {
		return Route{}, fmt.Errorf("route %q: want <app origin>,<card origin>", spec)
	}
	app, err := origin(a)
	if err != nil {
		return Route{}, err
	}
	cards, err := origin(c)
	if err != nil {
		return Route{}, err
	}
	if app == cards {
		return Route{}, fmt.Errorf("route %q: cards need their own origin", spec)
	}
	return Route{App: app, Cards: cards}, nil
}

// origin normalizes "https://host[:port]" the way a browser writes an
// Origin header: lowercase, no default port, no path.
func origin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q is not an origin like https://host or http://127.0.0.1:7457", s)
	}
	host := strings.ToLower(u.Host)
	if p := u.Port(); (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
		// Trim the port only, so an IPv6 host keeps its brackets.
		host = strings.TrimSuffix(host, ":"+p)
	}
	return u.Scheme + "://" + host, nil
}

// LoopbackPort checks that addr binds a loopback address and returns its port.
func LoopbackPort(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "" || port == "0" {
		return "", fmt.Errorf("%s is not a loopback address with a port; the web server binds 127.0.0.1 or ::1 only", addr)
	}
	return port, nil
}

// LoopbackRoutes are the routes a browser on this computer uses.
func LoopbackRoutes(appPort, cardPort string) []Route {
	return []Route{
		{App: "http://127.0.0.1:" + appPort, Cards: "http://127.0.0.1:" + cardPort},
		{App: "http://localhost:" + appPort, Cards: "http://localhost:" + cardPort},
	}
}

// LocalRoute reports whether rt is a loopback route: the browser is on
// this computer, not behind a tunnel or a proxy.
func LocalRoute(rt Route) bool {
	host := hostOf(rt.App)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func hostOf(o string) string { return o[strings.Index(o, "://")+3:] }

// FrameAncestors is the card CSP's frame-ancestors value: every app origin.
// It comes from configuration only, never from a request header. A wrong
// value fails closed: the card stays blank.
func FrameAncestors(routes []Route) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range routes {
		if !seen[r.App] {
			seen[r.App] = true
			out = append(out, r.App)
		}
	}
	return strings.Join(out, " ")
}
