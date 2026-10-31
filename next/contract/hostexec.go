package contract

import (
	"encoding/json"
	"errors"
	"time"
)

type HostexecGrantArgs struct {
	Room     RoomID        `json:"room"`
	To       Principal     `json:"to"`
	Hosts    []HostName    `json:"hosts"`
	Commands []CommandRule `json:"commands"`
	Mode     GrantMode     `json:"mode"`
	Expires  time.Time     `json:"expires,omitzero"`
	Never    bool          `json:"never"`
}

func (HostexecGrantArgs) OpKind() OpKind          { return "hostexec.grant" }
func (HostexecGrantArgs) isOpArgs()               {}
func (a HostexecGrantArgs) OperationRoom() RoomID { return a.Room }

type GrantExpired struct {
	Grant GrantID `json:"grant"`
}

func (GrantExpired) EventType() EventType { return "grant.expired" }
func (GrantExpired) isEventBody()         {}

// UnmarshalJSON validates external grant requests.
func (a *HostexecGrantArgs) UnmarshalJSON(data []byte) error {
	type plain HostexecGrantArgs
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Room == "" || v.To.Kind != PrincipalSession || len(v.Hosts) == 0 || len(v.Commands) == 0 || (v.Mode != GrantModeApprove && v.Mode != GrantModePreapproved) || v.Never && !v.Expires.IsZero() {
		return errors.New("invalid hostexec grant")
	}
	*a = HostexecGrantArgs(v)
	return nil
}
