package connect

import (
	"context"
	"net/url"
	"strings"
)

type reportedRoute struct {
	Kind  string `json:"kind"`
	App   string `json:"app"`
	Cards string `json:"cards"`
}

// reportRoutes tells the relay which configured app origins may receive /open.
func (r *Relay) reportRoutes(ctx context.Context, link Link, origins []string) error {
	routes := []reportedRoute{}
	for _, origin := range RankedRoutes(origins, link.App) {
		if strings.EqualFold(origin, link.App) {
			continue
		}
		u, _ := url.Parse(origin) // SetRoutes validated and normalized these origins.
		host := u.Hostname()
		if u.Scheme == "http" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
			continue
		}
		kind := [...]string{"loopback", "tailscale", "custom"}[routeRank(origin, link.App)]
		routes = append(routes, reportedRoute{kind, origin, link.Cards})
		if len(routes) == 8 {
			break
		}
	}
	return call(ctx, r, link.Machine, "/v1/machine/routes", struct {
		Machine string          `json:"machine"`
		Routes  []reportedRoute `json:"routes"`
	}{link.Machine, routes}, &struct{}{})
}
