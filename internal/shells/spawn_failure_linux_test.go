package shells

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// StartSupervisor starts the supervisor, then fails to record its PID. The
// supervisor must not start the job's command, and nothing may be left running.
// The fake supervisor starts a child that outlives it, and ignores SIGTERM, only
// after the launcher's go-ahead; otherwise it only leaves a marker.
func TestStartSupervisorNeverStartsTheCommandWhenItsPIDCannotBeRecorded(t *testing.T) {
	id := testJob(t)
	dir, err := DefaultStore().dir(id)
	if err != nil {
		t.Fatal(err)
	}
	// A directory in the PID file's place makes the write fail.
	if err := os.Mkdir(filepath.Join(dir, supervisorPID), 0o700); err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	started, refused := filepath.Join(marks, "started"), filepath.Join(marks, "refused")
	exe := filepath.Join(t.TempDir(), "dyna")
	script := "#!/bin/sh\n" +
		"if read -r go <&3; then\n" +
		"  env -i /bin/sh -c 'trap \"\" TERM; echo up > " + started + "; exec sleep 600' &\n" +
		"  wait\n" +
		"else\n" +
		"  echo no > " + refused + "\n" +
		"fi\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(launchGateEnv, "") // the test's own environment carries no gate
	if err := StartSupervisor(exe, id); err == nil {
		t.Fatal("StartSupervisor reported success although the PID could not be recorded")
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the command started although the launch failed")
	}
	if _, err := os.Stat(refused); err != nil {
		t.Fatalf("the supervisor was not refused at the gate: %v", err)
	}
}

// A supervisor whose launcher closes the gate without the go-ahead ends without
// starting its command.
func TestSuperviseDoesNotStartWhenTheLaunchGateClosesUnsent(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	marker := filepath.Join(t.TempDir(), "ran")
	meta, err := s.Create(StartOptions{Cmd: []string{"touch", marker}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	gateWrite.Close()
	// awaitLaunch reads the descriptor named by the environment; the test's pipe
	// is not descriptor 3, so name it by number.
	t.Setenv(launchGateEnv, itoa(int(gateRead.Fd())))
	if err := s.Supervise(context.Background(), meta.ID); err == nil {
		t.Fatal("Supervise started although the launch gate closed unsent")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}

// A supervisor that gets the go-ahead consumes the gate marker: its command does
// not inherit it, so a nested supervisor cannot read a stale descriptor.
func TestSuperviseDoesNotPassTheLaunchGateMarkerToTheCommand(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	seen := filepath.Join(t.TempDir(), "seen")
	meta, err := s.Create(StartOptions{Cmd: []string{"sh", "-c", "echo \"[${" + launchGateEnv + "-unset}]\" > " + seen}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateWrite.Write([]byte{'g'}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(launchGateEnv, itoa(int(gateRead.Fd())))
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[unset]\n" {
		t.Fatalf("the command saw the gate marker: %q", got)
	}
}

// A supervisor that ended before it read the go-ahead is not a launch failure:
// its error is kept in the job folder.
func TestSendGoAheadIgnoresAGateWhoseReaderEnded(t *testing.T) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateWrite.Close()
	gateRead.Close()
	if err := sendGoAhead(gateWrite); err != nil {
		t.Fatalf("sendGoAhead = %v; want nil for a closed gate", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
