package connect

import (
	"dyna-agent/internal/websvc"
	"time"
)

// Route is one way to reach this machine, with its kind: loopback,
// tailscale, custom or tunnel.
type ConnectRoute struct {
	Kind         string `json:"kind"`
	websvc.Route `tstype:",extends"`
}

// Renewal is the reply to a session renewal.
type Renewal struct {
	Expires  time.Time `json:"expires"`
	GrantEnd time.Time `json:"grantEnd"`
	Grace    bool      `json:"grace"`
}
type Handoff struct {
	Code string `json:"code"`
}
type Whoami struct {
	Kind     string     `json:"kind"`
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	User     *string    `json:"user,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"`
	GrantEnd *time.Time `json:"grantEnd,omitempty"`
}
type RouteList struct {
	Machine string         `json:"machine"`
	Key     string         `json:"key"`
	Current string         `json:"current"`
	Routes  []ConnectRoute `json:"routes"`
}
type ConnectSession struct {
	Session  string    `json:"session"`
	Expires  time.Time `json:"expires"`
	GrantEnd time.Time `json:"grantEnd"`
}
