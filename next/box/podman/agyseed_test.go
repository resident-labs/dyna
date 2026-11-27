package podman

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

func shell(t *testing.T, input []byte, script string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"-c", script, "dyna"}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Output()
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func agyFolder(t *testing.T) string {
	dir := t.TempDir()
	write(t, dir+"/antigravity", "#!/bin/sh\n", 0o755)
	write(t, dir+"/accounts/main/token.json", "TOKEN-ONE", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/conversations/s.db", "CONVERSATION", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/log/agy.log", "LOG", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/brain/c1/.system_generated/steps/o.txt", "BRAIN", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/mcp/github/x", "MCP", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/cache/x", "CACHE", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/implicit/x", "IMPLICIT", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/annotations/x", "ANNOT", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/builtin/skills/s", "BUILTIN", 0o600)
	write(t, dir+"/accounts/main/config/projects/p", "CONFIG", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/settings.json", "SETTINGS", 0o600)
	return dir
}

func TestAgyPackLeavesOutWhatSessionsWriteAndKeepsTheSignInAndConfig(t *testing.T) {
	src := agyFolder(t)
	pack, err := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tar", "-t", "-i", "-f", "-")
	cmd.Stdin = bytes.NewReader(pack)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	names := string(out)
	for _, want := range []string{"antigravity", "accounts/main/token.json", "accounts/main/antigravity-acp/settings.json", "builtin/skills/s", "config/projects/p", "mcp/github/x", "cache/x", "implicit/x"} {
		if !strings.Contains(names, want) {
			t.Errorf("the pack lacks %s: %s", want, names)
		}
	}
	for _, never := range []string{"conversations", "s.db", "agy.log", "/log", "brain", "BRAIN", "annotations", "ANNOT"} {
		if strings.Contains(names, never) {
			t.Errorf("the pack holds %s: %s", never, names)
		}
	}
}

func TestAgyUnpackPlacesTheFolderPrivatelyAndRefusesASeededHost(t *testing.T) {
	src := agyFolder(t)
	pack, err := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	want := strings.TrimSpace(string(sum))
	dst := t.TempDir() + "/agy"
	if _, err := shell(t, pack, agyUnpackScript, dst, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst + "/accounts/main")
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the account folder: %v %v", info, err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "TOKEN-ONE" {
		t.Fatalf("token %q", got)
	}
	if info, err := os.Stat(dst + "/antigravity"); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the program: %v %v", info, err)
	}
	if left, _ := filepath.Glob(dst + "/.seed.*"); len(left) != 1 { // only the lock file stays
		t.Fatalf("temporaries left: %v", left)
	}
	// A second seed of a changed source leaves the folder alone, and says so.
	write(t, src+"/accounts/main/token.json", "TOKEN-TWO", 0o600)
	pack, _ = shell(t, nil, agyPackScript, src, "accounts/main")
	_, err = shell(t, pack, agyUnpackScript, dst, "-")
	if exit := (*exec.ExitError)(nil); !errors.As(err, &exit) || exit.ExitCode() != agyUnpackExitSeeded {
		t.Fatalf("a seeded host: %v", err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "TOKEN-ONE" {
		t.Fatalf("an existing folder was overwritten: %q", got)
	}
}

func TestAgyUnpackWithAWrongChecksumPlacesNothing(t *testing.T) {
	src := agyFolder(t)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	dst := t.TempDir() + "/agy"
	if _, err := shell(t, pack, agyUnpackScript, dst, strings.Repeat("0", 64)); err == nil {
		t.Fatal("a wrong checksum was accepted")
	}
	for _, path := range []string{"/antigravity", "/accounts/main"} {
		if _, err := os.Stat(dst + path); err == nil {
			t.Fatalf("%s is present after a failed copy", path)
		}
	}
}

func TestAgyStateStatsWithoutOpeningTheFolder(t *testing.T) {
	dir := t.TempDir()
	run := func() string {
		out, err := shell(t, nil, agyStateScript, dir)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if out := run(); strings.Contains(out, "dyna-agy-binary") || strings.Contains(out, "dyna-agy-account") || !strings.Contains(out, "dyna-agy-arch Linux") {
		t.Fatalf("empty: %q", out)
	}
	write(t, dir+"/antigravity", "x", 0o755)
	write(t, dir+"/accounts/main/token.json", "x", 0o600)
	if err := os.Chmod(dir+"/accounts/main", 0o000); err != nil { // not even readable: only a stat may touch it
		t.Fatal(err)
	}
	defer os.Chmod(dir+"/accounts/main", 0o700)
	out := run()
	if !strings.Contains(out, "dyna-agy-binary") || !strings.Contains(out, "dyna-agy-account") || !strings.Contains(out, "dyna-agy-sum ") {
		t.Fatalf("both: %q", out)
	}
}

// shellIn runs a script with its own TMPDIR, so that a test can see every file the script puts under it.
func shellIn(t *testing.T, tmp string, input io.Reader, script string, args ...string) error {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"-c", script, "dyna"}, args...)...)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	cmd.Stdin = input
	return cmd.Run()
}

// holdsSentinel walks dir for a file holding the sentinel, or a file name that holds it.
func holdsSentinel(t *testing.T, dir, sentinel string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.Contains(path, sentinel) {
			found = append(found, path)
		}
		if !d.IsDir() {
			if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(sentinel)) {
				found = append(found, path)
			}
		}
		return nil
	})
	return found
}

// The pack goes from one script's stdout to the other's stdin, and no file of the archive is made under TMPDIR on either end, nor left in
// the agy folder; a cut stream installs nothing.
func TestAgyArchiveNeverRestsUnderTmp(t *testing.T) {
	src := agyFolder(t)
	write(t, src+"/accounts/main/token.json", "SENTINEL-ACCOUNT-BYTES", 0o600)
	tmp := t.TempDir()
	dst := t.TempDir() + "/agy"
	pack := exec.Command("bash", "-c", agyPackScript, "dyna", src, "antigravity", "accounts/main")
	pack.Env = append(os.Environ(), "TMPDIR="+tmp)
	out, err := pack.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Start(); err != nil {
		t.Fatal(err)
	}
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	// The unpack is handed the stream a little at a time, and TMPDIR is scanned while it is still waiting for the rest.
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- shellIn(t, tmp, reader, agyUnpackScript, dst, strings.TrimSpace(string(sum))) }()
	buffer := make([]byte, 512)
	n, _ := io.ReadFull(out, buffer)
	writer.Write(buffer[:n])
	if found := holdsSentinel(t, tmp, "SENTINEL-ACCOUNT-BYTES"); len(found) != 0 {
		t.Fatalf("archive bytes under TMPDIR during the copy: %v", found)
	}
	io.Copy(writer, out)
	writer.Close()
	if err := pack.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if found := holdsSentinel(t, tmp, "SENTINEL-ACCOUNT-BYTES"); len(found) != 0 {
		t.Fatalf("archive bytes under TMPDIR after the copy: %v", found)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("a temporary folder is left in the agy folder: %v", left)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "SENTINEL-ACCOUNT-BYTES" {
		t.Fatalf("the copy: %q", got)
	}
}

// A stream cut before the complete marker, as a pack that died leaves it, installs nothing and leaves nothing behind.
func TestAgyUnpackOfACutStreamInstallsNothing(t *testing.T) {
	src := agyFolder(t)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	cut := pack[:len(pack)-10240] // the marker's tar, one padded record, is the last of the stream
	dst := t.TempDir() + "/agy"
	if err := shellIn(t, t.TempDir(), bytes.NewReader(cut), agyUnpackScript, dst, strings.TrimSpace(string(sum))); err == nil {
		t.Fatal("a cut stream was installed")
	}
	for _, path := range []string{"/antigravity", "/accounts/main"} {
		if _, err := os.Stat(dst + path); err == nil {
			t.Fatalf("%s is present after a cut stream", path)
		}
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

// A copy killed outright leaves its temporary folder in the agy folder, and never under /tmp; the next seed sweeps it.
func TestAgyUnpackKilledLeavesNothingUnderTmpAndTheNextSeedSweeps(t *testing.T) {
	src := agyFolder(t)
	write(t, src+"/accounts/main/token.json", "SENTINEL-KILLED-BYTES", 0o600)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	tmp := t.TempDir()
	dst := t.TempDir() + "/agy"
	reader, writer := io.Pipe()
	cmd := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, strings.TrimSpace(string(sum)))
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	cmd.Stdin = reader
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	writer.Write(pack[:len(pack)/2])
	deadline := time.Now().Add(10 * time.Second)
	for {
		if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the copy never began")
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the whole group, as a dropped session ends it
	writer.Close()
	cmd.Wait()
	if found := holdsSentinel(t, tmp, "SENTINEL-KILLED-BYTES"); len(found) != 0 {
		t.Fatalf("archive bytes under TMPDIR after a killed copy: %v", found)
	}
	if err := shellIn(t, tmp, bytes.NewReader(pack), agyUnpackScript, dst, strings.TrimSpace(string(sum))); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("the next seed left the killed copy's folder: %v", left)
	}
}

// A program that appeared while the archive was on its way is not replaced.
func TestAgyUnpackLeavesAProgramInstalledMeanwhile(t *testing.T) {
	src := agyFolder(t)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	want := strings.TrimSpace(string(sum))
	dst := t.TempDir() + "/agy"
	write(t, dst+"/antigravity", "ANOTHER WRITER'S PROGRAM", 0o755)
	if err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/antigravity"); string(got) != "ANOTHER WRITER'S PROGRAM" {
		t.Fatalf("a program installed meanwhile was replaced: %q", got)
	}
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// agyArchive packs the test folder and returns the archive and the program's checksum.
func agyArchive(t *testing.T, token string) ([]byte, string) {
	t.Helper()
	src := agyFolder(t)
	write(t, src+"/accounts/main/token.json", token, 0o600)
	pack, err := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	return pack, strings.TrimSpace(string(sum))
}

// stubMv puts a stand-in for mv first on PATH that runs body (bash, with $main the account path) when the move is the one that publishes
// the account, and then the real mv. It returns the PATH to run the script with.
func stubMv(t *testing.T, dst, body string) string {
	t.Helper()
	real, _ := exec.LookPath("mv")
	bin := t.TempDir()
	write(t, bin+"/mv", "#!/bin/bash\nmain="+dst+"/accounts/main\ncase \"$*\" in *\".seed.\"*accounts/main*) "+body+" ;; esac\nexec "+real+" \"$@\"\n", 0o755)
	return bin + ":" + os.Getenv("PATH")
}

// stallBeforePublishing starts an unpack whose move of the account blocks until the test writes to the returned fifo, and returns once
// it has got there: the stub writes a ready file and then waits, so the test acts only after that, never after a guess.
func stallBeforePublishing(t *testing.T, dst, want string, pack []byte) (cmd *exec.Cmd, release string) {
	t.Helper()
	dir := t.TempDir()
	ready, release := dir+"/ready", dir+"/release"
	if err := syscall.Mkfifo(release, 0o600); err != nil {
		t.Fatal(err)
	}
	path := stubMv(t, dst, ": > "+ready+"; read -r _ < "+release)
	cmd = exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, want)
	cmd.Env = append(os.Environ(), "PATH="+path)
	cmd.Stdin = bytes.NewReader(pack)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			return cmd, release
		}
		if time.Now().After(deadline) {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatal("the seed never reached its publishing move")
		}
	}
}

// An account folder at the target, or a dangling link at its path, ends the seed with the seeded status before anything is extracted, and
// nothing changes.
func TestAgyUnpackRefusesWhateverIsAtTheAccountPath(t *testing.T) {
	pack, want := agyArchive(t, "NEW")
	for name, make := range map[string]func(main string){
		"a folder":        func(main string) { write(t, main+"/token.json", "OLD", 0o600) },
		"a dangling link": func(main string) { os.MkdirAll(filepath.Dir(main), 0o700); os.Symlink("/nowhere/at/all", main) },
		"a file":          func(main string) { write(t, main, "OLD", 0o600) },
	} {
		dst := t.TempDir() + "/agy"
		make(dst + "/accounts/main")
		before, _ := os.Lstat(dst + "/accounts/main")
		err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, want)
		if exitCode(err) != agyUnpackExitSeeded {
			t.Fatalf("%s: %v", name, err)
		}
		after, lerr := os.Lstat(dst + "/accounts/main")
		if lerr != nil || !os.SameFile(before, after) {
			t.Fatalf("%s: the path changed: %v", name, lerr)
		}
		if _, err := os.Stat(dst + "/antigravity"); err == nil {
			t.Fatalf("%s: a refused seed installed the program", name)
		}
		if left, _ := filepath.Glob(dst + "/.seed.*"); len(left) != 1 {
			t.Fatalf("%s: left %v", name, left)
		}
	}
}

// A seed killed after it published the program and before it published the account leaves no account folder at all, and the host does not
// report agy; the next seed finds the program already there, keeps it, sweeps the killed seed's folder and publishes the account.
func TestAgyInterruptedSeedLeavesNoAccountAndTheNextOneCompletesIt(t *testing.T) {
	pack, want := agyArchive(t, "TOKEN")
	dst := t.TempDir() + "/agy"
	cmd, _ := stallBeforePublishing(t, dst, want, pack)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	if _, err := os.Lstat(dst + "/accounts/main"); err == nil {
		t.Fatal("an interrupted seed left an account folder")
	}
	if info, err := os.Stat(dst + "/antigravity"); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the program is published first: %v %v", info, err)
	}
	probe := func() string {
		out, err := exec.Command("bash", "-c", agyScript, "dyna", "/data", "/mount", "gpu", "/bin/dyna", dst+"/antigravity", dst+"/accounts/main").CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	if got := probe(); got != "dyna-agy 0" {
		t.Fatalf("the probe before the account is published: %q", got)
	}
	state, _ := shell(t, nil, agyStateScript, dst)
	if !strings.Contains(string(state), "dyna-agy-binary") || strings.Contains(string(state), "dyna-agy-account") {
		t.Fatalf("state: %q", state)
	}
	// The program is on the host, so the next seed does not send it: it is asked for the account only.
	if err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, "-"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "TOKEN" {
		t.Fatalf("the account: %q", got)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("the killed seed's folder was not swept: %v", left)
	}
	if got := probe(); got != "dyna-agy 1" {
		t.Fatalf("the probe after publication: %q", got)
	}
}

// Of two seeds on one target, the one that takes the lock first publishes, and the other, started while it holds the lock, ends with the
// seeded status and changes nothing, whatever its source did.
func TestAgyTwoSeedsOnOneTargetPublishOnce(t *testing.T) {
	first, want := agyArchive(t, "FIRST")
	second, _ := agyArchive(t, "SECOND")
	dst := t.TempDir() + "/agy"
	a, release := stallBeforePublishing(t, dst, want, first)
	b := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, want)
	b.Stdin = bytes.NewReader(second)
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil { // the fifo's reader is the stub, which is waiting for it
		t.Fatal(err)
	}
	errA, errB := a.Wait(), b.Wait()
	if errA != nil || exitCode(errB) != agyUnpackExitSeeded {
		t.Fatalf("first: %v, second: %v", errA, errB)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "FIRST" {
		t.Fatalf("the account: %q", got)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

// mv -n succeeds when it declines, so a folder that appears between the last check and the rename ends the seed as seeded, found by the
// identity of the folder at the path and not by the move's status; what appeared stays.
func TestAgyAnAccountThatAppearsBeforeTheRenameIsNotTakenForOurs(t *testing.T) {
	pack, want := agyArchive(t, "OURS")
	dst := t.TempDir() + "/agy"
	cmd := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, want)
	cmd.Env = append(os.Environ(), "PATH="+stubMv(t, dst, `mkdir -p "$main"; echo THEIRS > "$main/token.json"`))
	cmd.Stdin = bytes.NewReader(pack)
	if err := cmd.Run(); exitCode(err) != agyUnpackExitSeeded {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "THEIRS\n" {
		t.Fatalf("the account: %q", got)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

// A program path that holds anything but an executable file ends the seed with the program status before the account is published, and
// nothing is created: a retry is not "seeded", and the owner is told to clear the path.
func TestAgyUnpackRefusesAnUnusableProgramBeforeTheAccount(t *testing.T) {
	pack, want := agyArchive(t, "NEW")
	for name, make := range map[string]func(bin string){
		"not executable":  func(bin string) { write(t, bin, "x", 0o644) },
		"a dangling link": func(bin string) { os.MkdirAll(filepath.Dir(bin), 0o700); os.Symlink("/nowhere/at/all", bin) },
		"a folder":        func(bin string) { os.MkdirAll(bin, 0o755) },
	} {
		dst := t.TempDir() + "/agy"
		make(dst + "/antigravity")
		err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, want)
		if exitCode(err) != agyUnpackExitProgram {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Lstat(dst + "/accounts/main"); err == nil {
			t.Fatalf("%s: the account was published beside an unusable program", name)
		}
		if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
			t.Fatalf("%s: left %v", name, left)
		}
	}
}

// An unusable program that appears between the check and the program's rename is found after it, and the account is still not
// published; a usable one that appears there is kept, and the seed goes on.
func TestAgyAProgramThatAppearsBeforeItsRenameIsNotTakenForOurs(t *testing.T) {
	pack, want := agyArchive(t, "NEW")
	for name, test := range map[string]struct {
		mode   string
		status int
	}{"unusable": {"0644", agyUnpackExitProgram}, "usable": {"0755", 0}} {
		dst := t.TempDir() + "/agy"
		real, _ := exec.LookPath("mv")
		bin := t.TempDir()
		write(t, bin+"/mv", "#!/bin/bash\ncase \"$*\" in *antigravity*) mkdir -p "+dst+"; echo THEIRS > "+dst+"/antigravity; chmod "+test.mode+" "+dst+"/antigravity ;; esac\nexec "+real+" \"$@\"\n", 0o755)
		cmd := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, want)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		cmd.Stdin = bytes.NewReader(pack)
		if err := cmd.Run(); exitCode(err) != test.status && !(test.status == 0 && err == nil) {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := os.ReadFile(dst + "/antigravity"); string(got) != "THEIRS\n" {
			t.Fatalf("%s: the program: %q", name, got)
		}
		_, err := os.Lstat(dst + "/accounts/main")
		if (err == nil) != (test.status == 0) {
			t.Fatalf("%s: account published = %v", name, err == nil)
		}
	}
}

// A file that changes while tar reads it ends the pack with the changed status, not as a good archive, and the target of a pipe from it
// installs nothing. The stub tar changes a file under a kept folder (mcp) and says so with tar's own status 1.
func TestAgyPackOfAFileThatChangesIsNotAGoodArchive(t *testing.T) {
	src := agyFolder(t)
	bin := t.TempDir()
	write(t, bin+"/tar", "#!/bin/bash\ncase \"$*\" in *dyna-complete*) exec /usr/bin/tar \"$@\" ;; esac\n/usr/bin/tar \"$@\"; echo MORE >> "+src+"/accounts/main/antigravity-acp/mcp/github/x; exit 1\n", 0o755)
	cmd := exec.Command("bash", "-c", agyPackScript, "dyna", src, "antigravity", "accounts/main")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if exitCode(err) != agyPackExitChanged {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(string(out), ".dyna-complete") {
		t.Fatal("a changed file still got the completion marker")
	}
	dst := t.TempDir() + "/agy"
	if err := shellIn(t, t.TempDir(), bytes.NewReader(out), agyUnpackScript, dst, "-"); err == nil {
		t.Fatal("the target took a cut archive")
	}
	if _, err := os.Lstat(dst + "/accounts/main"); err == nil {
		t.Fatal("an account was published")
	}
}

// streamRecorder is a host driver that streams nothing and keeps the command it was given.
type streamRecorder struct {
	*runner
	argv []string
}

func (s *streamRecorder) RunStream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	s.argv = args
	return nil
}

// Packing the account first only stops the program delaying the account read; it does not shorten tar's per-file changed-while-reading window.
func TestPackAgyReadsTheAccountBeforeTheProgram(t *testing.T) {
	rec := &streamRecorder{runner: &runner{}}
	h := newTestHost(t, rec.runner)
	h.runner = rec
	if err := h.PackAgy(context.Background(), box.Layout{}, true, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	command := rec.argv[len(rec.argv)-1]
	if !strings.HasSuffix(command, "accounts/main' 'antigravity'") {
		t.Fatalf("members in order: %q", command)
	}
}

// What a live session rewrites every few seconds is left out, so the seed succeeds while sessions run; a kept file that changes still ends
// it with the changed status.
func TestAgyPackLeavesOutWhatChangesEverySecondsAndKeepsTheRest(t *testing.T) {
	src := agyFolder(t)
	acp := src + "/accounts/main/antigravity-acp/"
	for _, name := range []string{"conversation_summaries.db", "conversation_summaries.db-shm", "conversation_summaries.db-wal", "jetbox_summaries_proto.pb", "bin/agentapi"} {
		write(t, acp+name, "LIVE", 0o600)
	}
	write(t, acp+"installation_id", "KEPT", 0o600)
	write(t, acp+"bin/other", "KEPT", 0o755)
	pack, err := shell(t, nil, agyPackScript, src, "accounts/main", "antigravity")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tar", "-t", "-i", "-f", "-")
	cmd.Stdin = bytes.NewReader(pack)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{"conversation_summaries", "jetbox_summaries", "agentapi"} {
		if strings.Contains(string(out), never) {
			t.Errorf("the pack holds %s: %s", never, out)
		}
	}
	for _, want := range []string{"installation_id", "bin/other"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the pack lacks %s: %s", want, out)
		}
	}
}

// A stub tar that, like the real one, calls a file changed while it is read (status 1) only when the file is among the members: a file
// under an --exclude is not read. The live files are excluded, so the pack succeeds; a kept one ends it with the changed status.
func TestAgyPackWithALiveFileChangingSucceedsOnlyWhenItIsExcluded(t *testing.T) {
	for name, want := range map[string]int{
		"conversation_summaries.db-wal": 0, "jetbox_summaries_proto.pb": 0, "bin/agentapi": 0, "annotations/x": 0, "brain/c1/y": 0,
		"antigravity-oauth-token": agyPackExitChanged, "mcp/dyna/schema.json": agyPackExitChanged,
	} {
		src := agyFolder(t)
		write(t, src+"/accounts/main/antigravity-acp/"+name, "LIVE", 0o600)
		bin := t.TempDir()
		write(t, bin+"/tar", "#!/bin/bash\ncase \"$*\" in *dyna-complete*) exec /usr/bin/tar \"$@\" ;; esac\nfor a; do p=${a#--exclude=accounts/main/antigravity-acp/}; if [ \"$p\" != \"$a\" ]; then case \""+name+"\" in \"$p\"|\"$p\"/*) exec /usr/bin/tar \"$@\" ;; esac; fi; done\n/usr/bin/tar \"$@\"; exit 1\n", 0o755)
		cmd := exec.Command("bash", "-c", agyPackScript, "dyna", src, "accounts/main", "antigravity")
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		_, err := cmd.Output()
		got := 0
		if err != nil {
			got = exitCode(err)
		}
		if got != want {
			t.Errorf("%s: exit %d, want %d (%v)", name, got, want, err)
		}
	}
}
