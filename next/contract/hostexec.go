package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type HostexecGrantArgs struct {
	Room    RoomID     `json:"room"`
	To      Principal  `json:"to"`
	Hosts   []HostName `json:"hosts"`
	Reason  string     `json:"reason"`
	Expires time.Time  `json:"expires,omitzero"`
	Never   bool       `json:"never"`
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
	if !utf8.Valid(data) {
		return errors.New("invalid hostexec grant")
	}
	type plain HostexecGrantArgs
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Room == "" || v.To.Kind != PrincipalSession || len(v.Hosts) == 0 || v.Reason == "" || !utf8.ValidString(v.Reason) || utf8.RuneCountInString(v.Reason) > 300 || strings.ContainsAny(v.Reason, "\r\n") || v.Never && !v.Expires.IsZero() {
		return errors.New("invalid hostexec grant")
	}
	*a = HostexecGrantArgs(v)
	return nil
}
