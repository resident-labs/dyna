package connect

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

// routeRank accepts only configured origins. Plain HTTP never leaves loopback.
func routeRank(origin, tunnel string) int {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return -1
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme == "http" && !loopback || u.Scheme != "http" && u.Scheme != "https" {
		return -1
	}
	switch {
	case loopback:
		return 0
	case origin == tunnel:
		return 3
	case strings.HasSuffix(host, ".ts.net"):
		return 1
	default:
		return 2
	}
}

func (s *Sessions) routeList(w http.ResponseWriter, r *http.Request, origin string, routes []string) {
	l, _, err := s.link(r.Context())
	if err != nil {
		fail(w, 404, "not linked")
		return
	}
	list := []string{}
	for _, app := range routes {
		if routeRank(app, l.App) >= 0 {
			list = append(list, app)
		}
	}
	slices.SortFunc(list, func(a, b string) int {
		if rank := routeRank(a, l.App) - routeRank(b, l.App); rank != 0 {
			return rank
		}
		return strings.Compare(a, b)
	})
	reply(w, 200, struct {
		Machine string   `json:"machine"`
		Current string   `json:"current"`
		Routes  []string `json:"routes"`
	}{l.Machine, origin, list})
}

func (s *Sessions) issueHandoff(w http.ResponseWriter, r *http.Request, origin, target, proof string, x store.ConnectSession, routes []string) {
	if x.Stale || target == origin || !slices.Contains(routes, target) || routeRank(target, "") < 0 {
		fail(w, 400, "invalid target")
		return
	}
	if !s.allow("handoff:"+x.ID, 10) {
		fail(w, 429, "too many route switches")
		return
	}
	if s.proof(r.Context(), proof, origin, r.URL.Path, x.JKT, x.Epoch, x.Generation) != nil {
		fail(w, 403, "invalid proof")
		return
	}
	code, err := randomSecret()
	if err != nil {
		fail(w, 503, "unavailable")
		return
	}
	s.mu.Lock()
	for k, v := range s.handoffs {
		if !s.now().Before(v.End) {
			delete(s.handoffs, k)
		}
	}
	s.handoffs[hashToken(code)] = handoffCode{x, target, s.now().Add(time.Minute)}
	s.mu.Unlock()
	reply(w, 200, struct {
		Code string `json:"code"`
	}{code})
}
