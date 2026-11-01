package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type HostexecGrantArgs struct {
	Room   RoomID     `json:"room"`
	To     Principal  `json:"to"`
	Hosts  []HostName `json:"hosts"`
	Reason string     `json:"reason"`
}

func (HostexecGrantArgs) OpKind() OpKind          { return "hostexec.grant" }
func (HostexecGrantArgs) isOpArgs()               {}
func (a HostexecGrantArgs) OperationRoom() RoomID { return a.Room }

type GrantExpired struct {
	Grant GrantID `json:"grant"`
}

func (GrantExpired) EventType() EventType { return "grant.expired" }
func (GrantExpired) isEventBody()         {}

// UnmarshalJSON validates external grant requests. A box may leave room and to empty; the engine fills them from the box connection.
func (a *HostexecGrantArgs) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("invalid hostexec grant")
	}
	type plain HostexecGrantArgs
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if (v.To.Kind != PrincipalSession && v.To.Kind != "") || len(v.Hosts) == 0 || v.Reason == "" || !utf8.ValidString(v.Reason) || utf8.RuneCountInString(v.Reason) > 300 || strings.ContainsAny(v.Reason, "\r\n") {
		return errors.New("invalid hostexec grant")
	}
	*a = HostexecGrantArgs(v)
	return nil
}
