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

func itoa(n int) string { return strconv.Itoa(n) }
