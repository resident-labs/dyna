// Package turnlock tells whether a session turn runs, from the session's
// turn.lock and the runner identity in its gate file. It sits below subagent
// so other services, such as computers, share the one probe.
package turnlock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// GateFile is the session file that names the runner of the current turn.
const GateFile = "gate.json"

// RunnerAlive checks the gate's runner identity, not the process that happens
// to retain an inherited descriptor. Unknown identities remain conservative.
func RunnerAlive(dir, id string) (alive, known bool) {
	var st struct {
		Pid  int `json:"pid"`
		Turn int `json:"turn"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, GateFile)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Pid <= 0 {
		return false, false
	}
	if st.Pid == os.Getpid() {
		return true, true
	} // InProcessSpawner.
	return RunnerMatches(st.Pid, id, st.Turn)
}

// RunnerMatches checks that pid runs the exact session turn, including
// before its steering gate exists. A caller that will signal it pins the
// process with a pidfd before this read.
func RunnerMatches(pid int, id string, turn int) (alive, known bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if os.IsNotExist(err) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	args := strings.Split(string(b), "\x00")
	for i := 1; i+3 < len(args); i++ {
		if args[i] == "subagent" && args[i+1] == "_turn" && args[i+2] == id && args[i+3] == strconv.Itoa(turn) {
			return true, true
		}
	}
	return false, true
}

// Held reports whether a turn of session id (whose directory is dir) runs.
// A runner holds turn.lock exclusively for the whole turn, so a shared probe
// fails only while one runs; an exclusive probe would also fail against
// another reader's probe. A descendant that inherited the lock from an older
// runner does not count once that runner is gone.
func Held(dir, id string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "turn.lock"), os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if err != syscall.EWOULDBLOCK {
			return false
		}
		alive, known := RunnerAlive(dir, id)
		return alive || !known
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
