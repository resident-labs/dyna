package podman

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const tokenSentinel = "SENTINEL-NEW-TOKEN-5d1e"

// useFolder is an agy folder with an old token, and a folder for stored copies with the outgoing account's.
func useFolder(t *testing.T) (dir, stored string) {
	t.Helper()
	dir = t.TempDir()
	write(t, dir+"/"+agyTokenRel, "OLD-TOKEN", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/conversations/c", "CONVERSATION", 0o600)
	stored = t.TempDir() + "/old-account"
	write(t, stored, "STALE-COPY", 0o600)
	return dir, stored
}

func twice(token string) []byte { return []byte(token + token) }

func TestInstallTakesBackSwapsAndComparesAndLeavesTheRest(t *testing.T) {
	dir, stored := useFolder(t)
	out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, stored, "23")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "dyna-agy-killed 0") || strings.Contains(string(out), tokenSentinel) {
		t.Fatalf("output %q", out)
	}
	if got, _ := os.ReadFile(dir + "/" + agyTokenRel); string(got) != tokenSentinel {
		t.Fatalf("token %q", got)
	}
	if info, _ := os.Stat(dir + "/" + agyTokenRel); info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v", info.Mode())
	}
	// The outgoing token went back to its stored copy, whole, privately.
	if got, _ := os.ReadFile(stored); string(got) != "OLD-TOKEN" {
		t.Fatalf("take-back %q", got)
	}
	if info, _ := os.Stat(stored); info.Mode().Perm() != 0o600 {
		t.Fatalf("take-back mode %v", info.Mode())
	}
	if got, _ := os.ReadFile(dir + "/accounts/main/antigravity-acp/conversations/c"); string(got) != "CONVERSATION" {
		t.Fatalf("the conversation folder changed: %q", got)
	}
	for _, folder := range []string{dir + "/accounts/main/antigravity-acp", filepath.Dir(stored)} {
		if left, _ := filepath.Glob(folder + "/.*-*"); len(left) != 0 {
			t.Fatalf("temporaries left in %s: %v", folder, left)
		}
	}
	// Without a take-back (a host that is not the engine host) nothing is copied.
	other, _ := useFolder(t)
	if _, err := shell(t, twice(tokenSentinel), agyInstallScript, other, "-", "23"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(other + "/" + agyTokenRel); string(got) != tokenSentinel {
		t.Fatalf("token %q", got)
	}
}

func TestInstallChangesNothingOnAShortStreamOrAMissingFolder(t *testing.T) {
	dir, stored := useFolder(t)
	_, err := shell(t, []byte("SHORT"), agyInstallScript, dir, stored, "23")
	if exitCode(err) != agyInstallExitSize {
		t.Fatalf("short stream: %v", err)
	}
	if got, _ := os.ReadFile(dir + "/" + agyTokenRel); string(got) != "OLD-TOKEN" {
		t.Fatalf("token changed: %q", got)
	}
	if got, _ := os.ReadFile(stored); string(got) != "STALE-COPY" {
		t.Fatalf("the stored copy was replaced before the swap could not be made: %q", got)
	}
	if left, _ := filepath.Glob(dir + "/accounts/main/antigravity-acp/.dyna-token.*"); len(left) != 0 {
		t.Fatalf("temporary left: %v", left)
	}
	_, err = shell(t, twice(tokenSentinel), agyInstallScript, t.TempDir(), "-", "23")
	if exitCode(err) != agyInstallExitNone {
		t.Fatalf("host with no account folder: %v", err)
	}
	// A take-back that cannot be made (no current token) stops before anything is changed.
	bare := t.TempDir()
	os.MkdirAll(bare+"/accounts/main/antigravity-acp", 0o700)
	_, err = shell(t, twice(tokenSentinel), agyInstallScript, bare, stored, "23")
	if exitCode(err) != agyInstallExitCopy {
		t.Fatalf("no current token to take back: %v", err)
	}
	if _, err := os.Stat(bare + "/" + agyTokenRel); !os.IsNotExist(err) {
		t.Fatalf("a token was installed: %v", err)
	}
}

func TestInstallFailsWhenTheSecondCopyDiffers(t *testing.T) {
	dir, _ := useFolder(t)
	_, err := shell(t, []byte(tokenSentinel+"SENTINEL-OTHER-TOKEN-5d1"), agyInstallScript, dir, "-", "23")
	if exitCode(err) != agyInstallExitCheck {
		t.Fatalf("a differing second copy: %v", err)
	}
}

func TestInstallEndsEveryAgyServerButNotALogin(t *testing.T) {
	dir, _ := useFolder(t)
	bin := t.TempDir() + "/antigravity"
	write(t, bin, "#!/bin/sh\nsleep 60\n", 0o755)
	start := func(args ...string) *exec.Cmd {
		cmd := exec.Command(bin, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
		return cmd
	}
	server := start("acp", "--gemini-dir", "/x")
	login := start("acp", "--login", "--gemini-dir", "/y")
	time.Sleep(200 * time.Millisecond) // the shell is running its script, whose command line is what matches
	out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "-", "23")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "dyna-agy-killed 1") {
		t.Fatalf("output %q", out)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the agy server was not ended")
	}
	if err := syscall.Kill(login.Process.Pid, 0); err != nil {
		t.Fatalf("the login was ended: %v", err)
	}
}

// A swap and a seed take the same lock: a swap waits while the lock is held. The wait is a bounded negative check.
func TestInstallWaitsForTheSeedsLock(t *testing.T) {
	dir, _ := useFolder(t)
	holder := exec.Command("bash", "-c", `exec 9> "$1/.seed.lock"; flock -x 9; echo held; read x`, "dyna", dir)
	in, _ := holder.StdinPipe()
	stdout, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Wait()
	buf := make([]byte, 5)
	if _, err := stdout.Read(buf); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "-", "23"); done <- err }()
	select {
	case <-done:
		t.Fatal("the swap ran while the lock was held")
	case <-time.After(400 * time.Millisecond):
	}
	in.Write([]byte("\n"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the swap did not run after the lock was released")
	}
	if got, _ := os.ReadFile(dir + "/" + agyTokenRel); string(got) != tokenSentinel {
		t.Fatalf("token %q", got)
	}
}

func TestTokenSameCompares(t *testing.T) {
	dir, _ := useFolder(t)
	if _, err := shell(t, []byte("OLD-TOKEN"), agyTokenSameScript, dir); err != nil {
		t.Fatalf("same: %v", err)
	}
	if _, err := shell(t, []byte("OTHER"), agyTokenSameScript, dir); exitCode(err) != agyTokenExitDiffer {
		t.Fatalf("different: %v", err)
	}
	if _, err := shell(t, []byte("x"), agyTokenSameScript, t.TempDir()); exitCode(err) != 2 {
		t.Fatalf("no token: %v", err)
	}
}

// A seed carries the sign-in with the rest of main: a host seeded later has the token the engine host's main holds, which is the active
// account's when no switch is held.
func TestAgyPackKeepsTheSignInFile(t *testing.T) {
	src := agyFolder(t)
	write(t, src+"/"+agyTokenRel, "ACTIVE-TOKEN", 0o600)
	pack, err := shell(t, nil, agyPackScript, src, "accounts/main")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tar", "-t", "-i", "-f", "-")
	cmd.Stdin = bytes.NewReader(pack)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), agyTokenRel) {
		t.Fatalf("the seed leaves out the sign-in: %v %s", err, out)
	}
}
