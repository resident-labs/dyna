package contract

import "time"

type GrantKind string

const (
	GrantHostExec GrantKind = "hostexec"  // run commands on hosts (section 6)
	GrantAskOwner GrantKind = "ask-owner" // open owner questions and terminal requests
	GrantRead     GrantKind = "read"      // members: read the room
	GrantPost     GrantKind = "post"      // members: post in the room
	GrantApprove  GrantKind = "approve"   // members: approve agent-class operations
)

type GrantMode string // "approve" | "preapproved" (host exec only)

type Grant struct {
	ID       GrantID       `json:"id"`
	Kind     GrantKind     `json:"kind"`
	To       Principal     `json:"to"`
	Room     RoomID        `json:"room"`
	Hosts    []HostName    `json:"hosts,omitempty"`    // host exec: names, or AllHosts
	Commands []CommandRule `json:"commands,omitempty"` // host exec
	Mode     GrantMode     `json:"mode,omitempty"`     // host exec
	Expires  time.Time     `json:"expires,omitzero"`   // zero: no expiry
	By       Principal     `json:"by"`
	Created  time.Time     `json:"created"`
	Revoked  time.Time     `json:"revoked,omitzero"`
}

type CommandRule struct {
	Program string   `json:"program"`        // exact name or path; "*" matches any program
	Args    []string `json:"args,omitempty"` // exact leading arguments
	Prefix  bool     `json:"prefix"`         // more arguments may follow
}
