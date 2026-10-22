package subagent

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repairStore returns a store and a session that has a complete room stamp and
// an empty room log, so a repair can measure the input cutoff.
func repairStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, id := turnStore(t)
	if err := s.writeMeta(Meta{ID: id, RoomID: "room_20261021-120000-aaaaaaaa", Room: "work", Profile: "mock", Turns: 1}); err != nil {
		t.Fatal(err)
	}
	return s, id
}

// A turn whose runner process is still alive is never repaired, even with
// turn.lock free and the row not finished: a runner releases the lock before
// it exits. Once the runner is gone, the repair goes ahead and records what
// it read and who repaired.
func TestRepairWaitsForALiveRunner(t *testing.T) {
	s, id := repairStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	dir, err := s.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	// A process whose command line names this turn's runner, as the gate's
	// liveness check reads it. Until its exec, a started child still shows
	// this test's command line, so the test waits for the shell's first line.
	runner := exec.Command("/bin/sh", "-c", "echo ready; read never", "subagent", "_turn", id, "1")
	stdin, err := runner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := runner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(); err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = runner.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := writeGateState(dir, gateState{Active: false, Pid: runner.Process.Pid, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if repaired, err := s.RepairTurn(id, 1, "test repair"); err != nil || repaired {
		t.Fatalf("RepairTurn with a live runner = %v, %v; want no repair", repaired, err)
	}
	if turn, err := s.ReadTurn(id, 1); err != nil || turn.Status != TurnRunning {
		t.Fatalf("turn = %q, %v; want it untouched", turn.Status, err)
	}
	_ = stdin.Close()
	_ = runner.Wait()
	if repaired, err := s.RepairTurn(id, 1, "test repair"); err != nil || !repaired {
		t.Fatalf("RepairTurn after the runner exited = %v, %v; want a repair", repaired, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["t"] == "turn_repaired" {
			found = ev["readStatus"] == TurnRunning && ev["process"] != "" && ev["caller"] != ""
		}
	}
	if !found {
		t.Fatalf("no turn_repaired event with what the repair read: %s", raw)
	}
}

// A gate PID that a different process now uses, as after PID reuse, does
// not keep a dead turn from its repair: the liveness check requires the
// runner's own command line for this session and turn.
func TestRepairIgnoresAReusedRunnerPID(t *testing.T) {
	s, id := repairStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	dir, err := s.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	other := exec.Command("/bin/sh", "-c", "read never")
	stdin, err := other.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = other.Wait() })
	if err := writeGateState(dir, gateState{Pid: other.Process.Pid, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if repaired, err := s.RepairTurn(id, 1, "test repair"); err != nil || !repaired {
		t.Fatalf("RepairTurn with a reused gate PID = %v, %v; want a repair", repaired, err)
	}
}
