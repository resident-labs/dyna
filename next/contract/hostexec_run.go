package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

const (
	MethodHostexecList Method    = "hostexec.list"
	MethodHostexecRun  Method    = "hostexec.run"
	MethodHostexecStop Method    = "hostexec.stop"
	ErrNoGrant         ErrorCode = "no_grant"
)

// HostexecRunRequest runs Command on Host with bash -c.
type HostexecRunRequest struct {
	Host     HostName   `json:"host"`
	Command  string     `json:"command"`
	Cwd      string     `json:"cwd"`
	Timeout  DurationMS `json:"timeout"`
	ClientID string     `json:"clientId,omitempty"` // at most 64 characters; names the run for HostexecStopRequest
}
type HostexecRunResponse struct {
	Operation Operation `json:"operation"`
}

// HostexecStopRequest stops the caller's own run that its HostexecRunRequest named ClientID. A call that only went away
// stops nothing, since the box's connection to the engine can drop while the run is fine.
type HostexecStopRequest struct {
	ClientID string `json:"clientId"`
}
type HostexecStopResponse struct{}
type HostexecListRequest struct{}
type HostexecListResponse struct {
	Grants []Grant `json:"grants"`
}

type HostexecRunArgs struct {
	Room    RoomID     `json:"room"`
	Host    HostName   `json:"host"`
	Argv    []string   `json:"argv"`
	Cwd     string     `json:"cwd"`
	Timeout DurationMS `json:"timeout"`
	Grant   GrantID    `json:"grant"`
	// Desktop and Collect are set by the engine as it runs, never stored or sent: Desktop imports the user's graphical
	// session environment (an exec-only host), and Collect is the run that reads back and removes a run's $DYNA_OUT.
	Desktop bool `json:"-"`
	Collect bool `json:"-"`
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
