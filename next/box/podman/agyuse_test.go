package podman

import (
	"bytes"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const tokenSentinel = "SENTINEL-NEW-TOKEN-5d1e"

// useFolder is an agy folder with an old token and a conversation.
func useFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir+"/"+agyTokenRel, "OLD-TOKEN", 0o600)
	write(t, dir+"/accounts/main/antigravity-acp/conversations/c", "CONVERSATION", 0o600)
	return dir
}

func twice(token string) []byte { return []byte(token + token) }

// An install replaces the token and nothing else, and the live token it replaces is copied nowhere.
func TestInstallSwapsComparesAndLeavesTheRest(t *testing.T) {
	dir := useFolder(t)
	out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
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
	if got, _ := os.ReadFile(dir + "/accounts/main/antigravity-acp/conversations/c"); string(got) != "CONVERSATION" {
		t.Fatalf("the conversation folder changed: %q", got)
	}
	if left, _ := filepath.Glob(dir + "/accounts/main/antigravity-acp/.*-*"); len(left) != 0 {
		t.Fatalf("temporaries left: %v", left)
	}
	filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if data, _ := os.ReadFile(path); err == nil && !entry.IsDir() && strings.Contains(string(data), "OLD-TOKEN") {
			t.Errorf("the replaced token was copied to %s", path)
		}
		return nil
	})
}

func TestInstallChangesNothingOnAShortStreamOrAMissingFolder(t *testing.T) {
	dir := useFolder(t)
	_, err := shell(t, []byte("SHORT"), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
	if exitCode(err) != agyInstallExitSize {
		t.Fatalf("short stream: %v", err)
	}
	if got, _ := os.ReadFile(dir + "/" + agyTokenRel); string(got) != "OLD-TOKEN" {
		t.Fatalf("token changed: %q", got)
	}
	if left, _ := filepath.Glob(dir + "/accounts/main/antigravity-acp/.dyna-token.*"); len(left) != 0 {
		t.Fatalf("temporary left: %v", left)
	}
	_, err = shell(t, twice(tokenSentinel), agyInstallScript, t.TempDir(), "23", box.AgyBinary, contract.AgyGeminiDir)
	if exitCode(err) != agyInstallExitNone {
		t.Fatalf("host with no account folder: %v", err)
	}
	// A host with the folder and no token yet gets it.
	bare := t.TempDir()
	os.MkdirAll(bare+"/accounts/main/antigravity-acp", 0o700)
	if _, err = shell(t, twice(tokenSentinel), agyInstallScript, bare, "23", box.AgyBinary, contract.AgyGeminiDir); err != nil {
		t.Fatalf("no current token: %v", err)
	}
	if got, _ := os.ReadFile(bare + "/" + agyTokenRel); string(got) != tokenSentinel {
		t.Fatalf("token %q", got)
	}
}

func TestInstallFailsWhenTheSecondCopyDiffers(t *testing.T) {
	dir := useFolder(t)
	_, err := shell(t, []byte(tokenSentinel+"SENTINEL-OTHER-TOKEN-5d1"), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
	if exitCode(err) != agyInstallExitCheck {
		t.Fatalf("a differing second copy: %v", err)
	}
}

// The kill selects by the box's whole command line and nothing else: a box's agy server is ended, and a sign-in's login (the same program,
// --login, another folder), an agy with another account folder, a shell whose arguments merely contain the text, and an unrelated process
// are left alone. Each fake runs a shell with exactly the argv it stands for.
func TestInstallEndsOnlyTheBoxesAgyServer(t *testing.T) {
	dir := useFolder(t)
	work := t.TempDir()
	write(t, work+"/acp", "sleep 60\n", 0o644)
	start := func(argv ...string) *exec.Cmd {
		cmd := &exec.Cmd{Path: "/bin/sh", Args: argv, Dir: work}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
		return cmd
	}
	server := start(box.AgyBinary, "acp", "--gemini-dir", contract.AgyGeminiDir, "--app-data-dir", "antigravity-acp")
	survivors := map[string]*exec.Cmd{
		"login":         start(box.AgyBinary, "acp", "--login", "--gemini-dir", "/data/agy/login-abc", "--app-data-dir", "antigravity-acp"),
		"login on host": start("/data/agy/antigravity", "acp", "--login", "--gemini-dir", "/data/agy/login-def", "--app-data-dir", "antigravity-acp"),
		"other account": start(box.AgyBinary, "acp", "--gemini-dir", "/opt/agy/other", "--app-data-dir", "antigravity-acp"),
		"extra flag":    start(box.AgyBinary, "acp", "--gemini-dir", contract.AgyGeminiDir, "--app-data-dir", "antigravity-acp", "--login"),
		"text in argv":  start("/bin/sh", "-c", "sleep 60 # antigravity acp --gemini-dir "+contract.AgyGeminiDir+" --app-data-dir antigravity-acp"),
	}
	time.Sleep(300 * time.Millisecond) // the shells have exec'd: their command lines are what the script reads
	out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
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
		t.Fatal("the box's agy server was not ended")
	}
	for name, cmd := range survivors {
		if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
			t.Fatalf("%s was ended: %v", name, err)
		}
	}
}

// A swap and a seed take the same lock: a swap waits while the lock is held. The wait is a bounded negative check.
func TestInstallWaitsForTheSeedsLock(t *testing.T) {
	dir := useFolder(t)
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
	go func() {
		_, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
		done <- err
	}()
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

// The comparison with the host's token is made by the script, under the lock: a host that has the incoming token already does nothing and
// does nothing at all.
func TestInstallDoesNothingWhenTheHostHasTheTokenAlready(t *testing.T) {
	dir := useFolder(t)
	write(t, dir+"/"+agyTokenRel, tokenSentinel, 0o600)
	out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "dyna-agy-same") || strings.Contains(string(out), "dyna-agy-killed") {
		t.Fatalf("output %q", out)
	}
}

// An older install that finishes between the engine's decision and this script's lock must not let this run copy the incoming account's live
// token anywhere: the comparison is made after the lock is taken. A holder stands for the older install: it
// holds the lock, and on release writes the incoming token into place.
func TestInstallComparesAfterTheLockSoALateFinishedSwapIsLeftAlone(t *testing.T) {
	dir := useFolder(t)
	holder := exec.Command("bash", "-c", `exec 9> "$1/.seed.lock"; flock -x 9; echo held; read x; printf %s "$3" > "$1/$2"`, "dyna", dir, agyTokenRel, tokenSentinel)
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
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
		done <- result{out, err}
	}()
	select {
	case <-done:
		t.Fatal("the script ran while the lock was held")
	case <-time.After(400 * time.Millisecond): // bounded negative wait: the script is waiting for the lock
	}
	in.Write([]byte("\n"))
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(string(got.out), "dyna-agy-same") {
			t.Fatalf("the late run: %v %q", got.err, got.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the script did not run after the lock was released")
	}
}

// A process that survives the kill fails the host, and nothing is swapped. DYNA_AGY_KILL is the test seam: a kill that does
// nothing.
func TestInstallFailsWhenAnAgyServerSurvivesTheKill(t *testing.T) {
	dir := useFolder(t)
	work := t.TempDir()
	write(t, work+"/acp", "sleep 60\n", 0o644)
	cmd := &exec.Cmd{Path: "/bin/sh", Args: []string{box.AgyBinary, "acp", "--gemini-dir", contract.AgyGeminiDir, "--app-data-dir", "antigravity-acp"}, Dir: work}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	t.Setenv("DYNA_AGY_KILL", "true")
	_, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
	if exitCode(err) != agyInstallExitKill {
		t.Fatalf("a surviving server: %v", err)
	}
	if got, _ := os.ReadFile(dir + "/" + agyTokenRel); string(got) != "OLD-TOKEN" {
		t.Fatalf("token changed: %q", got)
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

// The account folder is writable from an agy box, so an agent can plant a link at the token path or on the way to it: the host fails, nothing
// is installed, and the link's target is neither read nor changed.
func TestInstallRefusesALinkedTokenOrFolder(t *testing.T) {
	for _, what := range []string{"token", "folder", "accounts"} {
		dir := useFolder(t)
		outside := t.TempDir() + "/unrelated"
		write(t, outside, "UNRELATED-FILE", 0o600)
		token := dir + "/" + agyTokenRel
		switch what {
		case "token":
			os.Remove(token)
			os.Symlink(outside, token)
		case "folder":
			elsewhere := t.TempDir()
			write(t, elsewhere+"/antigravity-oauth-token", "UNRELATED-FILE", 0o600)
			os.RemoveAll(dir + "/accounts/main/antigravity-acp")
			os.Symlink(elsewhere, dir+"/accounts/main/antigravity-acp")
			outside = elsewhere + "/antigravity-oauth-token"
		case "accounts":
			elsewhere := t.TempDir()
			os.MkdirAll(elsewhere+"/main/antigravity-acp", 0o700)
			write(t, elsewhere+"/main/antigravity-acp/antigravity-oauth-token", "UNRELATED-FILE", 0o600)
			os.RemoveAll(dir + "/accounts")
			os.Symlink(elsewhere, dir+"/accounts")
			outside = elsewhere + "/main/antigravity-acp/antigravity-oauth-token"
		}
		_, err := shell(t, twice(tokenSentinel), agyInstallScript, dir, "23", box.AgyBinary, contract.AgyGeminiDir)
		if exitCode(err) != agyInstallExitLink {
			t.Fatalf("%s link: %v", what, err)
		}
		if got, _ := os.ReadFile(outside); string(got) != "UNRELATED-FILE" {
			t.Fatalf("%s link: the link's target changed: %q", what, got)
		}
	}
}
