package podman

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	cmd := exec.Command("tar", "-t", "-f", "-")
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
