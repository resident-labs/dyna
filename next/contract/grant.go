package contract

import "time"

type GrantKind string

const (
	GrantHostExec GrantKind = "hostexec"  // run commands on hosts (section 6)
	GrantAskOwner GrantKind = "ask-owner" // open owner questions and show_pty requests
)

type Grant struct {
	ID      GrantID    `json:"id"`
	Kind    GrantKind  `json:"kind"`
	To      Principal  `json:"to"`
	Room    RoomID     `json:"room"`
	Hosts   []HostName `json:"hosts,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Expires time.Time  `json:"expires,omitzero"`
	By      Principal  `json:"by"`
	Created time.Time  `json:"created"`
	Revoked time.Time  `json:"revoked,omitzero"`
}
