package podman

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	write(t, dir+"/accounts/main/antigravity-acp/settings.json", "SETTINGS", 0o600)
	return dir
}

func TestAgyPackLeavesOutConversationsAndLogsByPath(t *testing.T) {
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
	for _, want := range []string{"antigravity", "accounts/main/token.json", "accounts/main/antigravity-acp/settings.json"} {
		if !strings.Contains(names, want) {
			t.Errorf("the pack lacks %s: %s", want, names)
		}
	}
	for _, never := range []string{"conversations", "s.db", "agy.log", "/log"} {
		if strings.Contains(names, never) {
			t.Errorf("the pack holds %s: %s", never, names)
		}
	}
}

func TestAgyUnpackPlacesTheFolderPrivatelyAndNeverOverwritesUnlessAsked(t *testing.T) {
	src := agyFolder(t)
	pack, err := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	want := strings.TrimSpace(string(sum))
	dst := t.TempDir() + "/agy"
	if _, err := shell(t, pack, agyUnpackScript, dst, want, "0"); err != nil {
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
	// A second seed of a changed source leaves the folder alone.
	write(t, src+"/accounts/main/token.json", "TOKEN-TWO", 0o600)
	pack, _ = shell(t, nil, agyPackScript, src, "accounts/main")
	if _, err := shell(t, pack, agyUnpackScript, dst, "-", "0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "TOKEN-ONE" {
		t.Fatalf("an existing folder was overwritten: %q", got)
	}
	// Asked to replace, it is replaced.
	if _, err := shell(t, pack, agyUnpackScript, dst, "-", "1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "TOKEN-TWO" {
		t.Fatalf("replace kept %q", got)
	}
}

func TestAgyUnpackWithAWrongChecksumPlacesNothing(t *testing.T) {
	src := agyFolder(t)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	dst := t.TempDir() + "/agy"
	if _, err := shell(t, pack, agyUnpackScript, dst, strings.Repeat("0", 64), "0"); err == nil {
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
	go func() { done <- shellIn(t, tmp, reader, agyUnpackScript, dst, strings.TrimSpace(string(sum)), "0") }()
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
	cut := pack[:len(pack)-10240] // the marker's tar, one padded record, is the last of the stream
	dst := t.TempDir() + "/agy"
	if err := shellIn(t, t.TempDir(), bytes.NewReader(cut), agyUnpackScript, dst, "-", "0"); err == nil {
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
	tmp := t.TempDir()
	dst := t.TempDir() + "/agy"
	reader, writer := io.Pipe()
	cmd := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, "-", "0")
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
	if err := shellIn(t, tmp, bytes.NewReader(pack), agyUnpackScript, dst, "-", "0"); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(dst + "/.seed.??????"); len(left) != 0 {
		t.Fatalf("the next seed left the killed copy's folder: %v", left)
	}
}

// A program that appeared while the archive was on its way is not replaced, unless the seed was asked to replace.
func TestAgyUnpackLeavesAProgramInstalledMeanwhile(t *testing.T) {
	src := agyFolder(t)
	pack, _ := shell(t, nil, agyPackScript, src, "antigravity", "accounts/main")
	sum, _ := shell(t, nil, `sha256sum < "$1" | cut -d ' ' -f 1`, src+"/antigravity")
	want := strings.TrimSpace(string(sum))
	dst := t.TempDir() + "/agy"
	write(t, dst+"/antigravity", "ANOTHER WRITER'S PROGRAM", 0o755)
	if err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, want, "0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/antigravity"); string(got) != "ANOTHER WRITER'S PROGRAM" {
		t.Fatalf("a program installed meanwhile was replaced: %q", got)
	}
	if err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, want, "1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/antigravity"); string(got) != "#!/bin/sh\n" {
		t.Fatalf("replace kept %q", got)
	}
}

// stallBeforePublishing runs an unpack with --replace whose publishing move blocks, so a test can end it exactly between the old
// account leaving its place and the new one arriving. It returns when the old folder has been moved aside.
func stallBeforePublishing(t *testing.T, dst string, pack []byte) *exec.Cmd {
	t.Helper()
	real, _ := exec.LookPath("mv")
	bin := t.TempDir()
	write(t, bin+"/mv", "#!/bin/bash\ncase \"$*\" in *\".seed.\"*accounts/main*) sleep 60 ;; esac\nexec "+real+" \"$@\"\n", 0o755)
	cmd := exec.Command("bash", "-c", agyUnpackScript, "dyna", dst, "-", "1")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdin = bytes.NewReader(pack)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dst + "/accounts/.main.old"); err == nil {
			if _, err := os.Stat(dst + "/accounts/main"); err != nil {
				return cmd
			}
		}
		if time.Now().After(deadline) {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatal("the replace never reached its publishing move")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A signal after the old account has left its place and before the new one is in it puts the old one back; it is never deleted.
func TestAgyReplaceSignalledBeforePublishingRestoresTheOldAccount(t *testing.T) {
	src := agyFolder(t)
	write(t, src+"/accounts/main/token.json", "NEW-ACCOUNT", 0o600)
	pack, _ := shell(t, nil, agyPackScript, src, "accounts/main")
	dst := t.TempDir() + "/agy"
	write(t, dst+"/accounts/main/token.json", "OLD-ACCOUNT", 0o600)
	cmd := stallBeforePublishing(t, dst, pack)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	cmd.Wait()
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "OLD-ACCOUNT" {
		t.Fatalf("the old account after a signal: %q", got)
	}
	if _, err := os.Stat(dst + "/accounts/.main.old"); err == nil {
		t.Fatal("a stray .main.old remains beside a restored account")
	}
}

// A kill that leaves no chance to clean up leaves the old account at .main.old, and the next seed, whatever it is asked, puts it back
// instead of deleting it.
func TestAgyReplaceKilledBeforePublishingIsRestoredByTheNextSeed(t *testing.T) {
	src := agyFolder(t)
	write(t, src+"/accounts/main/token.json", "NEW-ACCOUNT", 0o600)
	pack, _ := shell(t, nil, agyPackScript, src, "accounts/main")
	dst := t.TempDir() + "/agy"
	write(t, dst+"/accounts/main/token.json", "OLD-ACCOUNT", 0o600)
	cmd := stallBeforePublishing(t, dst, pack)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	cmd.Wait()
	if _, err := os.Stat(dst + "/accounts/main"); err == nil {
		t.Fatal("the kill left an account in place; the test did not stop between the moves")
	}
	if err := shellIn(t, t.TempDir(), bytes.NewReader(pack), agyUnpackScript, dst, "-", "0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst + "/accounts/main/token.json"); string(got) != "OLD-ACCOUNT" {
		t.Fatalf("the next seed did not restore the old account: %q", got)
	}
	if _, err := os.Stat(dst + "/accounts/.main.old"); err == nil {
		t.Fatal("a stray .main.old remains")
	}
}
