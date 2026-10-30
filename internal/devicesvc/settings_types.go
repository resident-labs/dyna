package devicesvc

import "time"

type Device struct {
	Current    bool      `json:"current"`
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Created    time.Time `json:"created"`
	LastSeen   time.Time `json:"lastSeen"`
	Expires    time.Time `json:"expires"`
	Origin     string    `json:"origin,omitempty"`
	Device     string    `json:"device,omitempty"`
	Scopes     []string  `json:"scopes,omitempty"`
	Legacy     bool      `json:"legacy,omitempty"`
	ScopeLabel string    `json:"scopeLabel,omitempty"`
}

type Pairing struct {
	Code    string    `json:"code"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}
