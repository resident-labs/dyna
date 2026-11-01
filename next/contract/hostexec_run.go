package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

const (
	MethodHostexecRun Method    = "hostexec.run"
	ErrNoGrant        ErrorCode = "no_grant"
)

type HostexecRunRequest struct {
	Host    HostName   `json:"host"`
	Argv    []string   `json:"argv"`
	Cwd     string     `json:"cwd"`
	Timeout DurationMS `json:"timeout"`
}
type HostexecRunResponse struct {
	Operation Operation `json:"operation"`
}
type HostexecRunArgs struct {
	Room    RoomID     `json:"room"`
	Host    HostName   `json:"host"`
	Argv    []string   `json:"argv"`
	Cwd     string     `json:"cwd"`
	Timeout DurationMS `json:"timeout"`
	Grant   GrantID    `json:"grant"`
}

func (HostexecRunArgs) OpKind() OpKind          { return "hostexec.run" }
func (HostexecRunArgs) isOpArgs()               {}
func (a HostexecRunArgs) OperationRoom() RoomID { return a.Room }

func (a *HostexecRunArgs) UnmarshalJSON(data []byte) error {
	type plain HostexecRunArgs
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if v.Room == "" || v.Host == "" || len(v.Argv) == 0 || v.Argv[0] == "" || v.Timeout <= 0 || v.Timeout > DurationMS((2*time.Hour).Milliseconds()) || v.Grant == "" {
		return errors.New("invalid hostexec run")
	}
	*a = HostexecRunArgs(v)
	return nil
}
