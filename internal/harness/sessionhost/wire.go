package sessionhost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"dyna-agent/internal/processguard"
)

// privateArg selects the host entrypoint, as in processguard: the exact
// argument vector [exe, privateArg, socket path], never an environment mark.
const privateArg = "--dyna-private-sessionhost-v1"

// Message ops. A client sends a control op and reads its opOK reply; the
// host pushes opFrame and opExit between replies. During attach and replace
// the host first sends opBind and waits for the client's opBound.
const (
	opAttach   = "attach"
	opReplace  = "replace"
	opBound    = "bound"
	opWrite    = "write"
	opDetach   = "detach"
	opClose    = "close"
	opCloseKey = "close-key"
	opHalt     = "halt"
	opKill     = "kill"
	opStderr   = "stderr"
	opShutdown = "shutdown"

	opBind  = "bind"
	opOK    = "ok"
	opFrame = "frame"
	opExit  = "exit"
)

// message is one JSON line on the host socket. Frame bytes ride in Data.
type message struct {
	Key        string                  `json:"key,omitempty"`
	Connection []byte                  `json:"connection,omitempty"`
	Op         string                  `json:"op"`
	Launch     *wireLaunch             `json:"launch,omitempty"`
	Ref        *processguard.Reference `json:"ref,omitempty"`
	Data       []byte                  `json:"data,omitempty"`
	Reason     DetachReason            `json:"reason,omitempty"`
	Pid        int                     `json:"pid,omitempty"`
	Started    bool                    `json:"started,omitempty"`
	// Fresh marks the reply to a replace: frames and failures queued before
	// it belong to the old child.
	Fresh bool   `json:"fresh,omitempty"`
	Error string `json:"error,omitempty"`
}

// wireLaunch is the part of Launch the host process needs.
type wireLaunch struct {
	Connection  []byte   `json:"connection,omitempty"`
	Harness     string   `json:"harness"`
	Key         string   `json:"key"`
	Fingerprint string   `json:"fingerprint"`
	Argv        []string `json:"argv"`
	Env         []string `json:"env"`
	Dir         string   `json:"dir"`
}

func toWire(l Launch) *wireLaunch {
	return &wireLaunch{Connection: l.Connection, Harness: l.Harness, Key: l.Key, Fingerprint: l.Fingerprint, Argv: l.Argv, Env: l.Env, Dir: l.Dir}
}

func (w wireLaunch) launch() Launch {
	return Launch{Connection: w.Connection, Harness: w.Harness, Key: w.Key, Fingerprint: w.Fingerprint, Argv: w.Argv, Env: w.Env, Dir: w.Dir}
}

// id is the registry identity of a launch: the harness namespace and the
// caller's key, so one harness never reuses another's child.
func (l Launch) id() string { return l.Harness + "\x00" + l.Key }

// hostPaths names an id's socket and lock inside dir. The name is a hash, so
// it stays short and leaks no session name.
func hostPaths(dir, id string) (socket, lock string) {
	sum := sha256.Sum256([]byte(id))
	name := filepath.Join(dir, hex.EncodeToString(sum[:8]))
	return name + ".sock", name + ".lock"
}

// lockFile takes the exclusive lock that serializes spawn and exit of a
// key's host, and returns its release.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock session host: %w", err)
	}
	return func() { f.Close() }, nil
}
