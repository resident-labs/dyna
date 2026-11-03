package hostexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// bashOf runs the wrapper of RunArgs without systemd-run, which a test machine may lack: a fake one runs what follows "--".
func bashOf(t *testing.T, a contract.HostexecRunArgs, op contract.OpID, env ...string) string {
	t.Helper()
	bin := t.TempDir()
	fake := "#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/bash\necho DISPLAY=:7\necho WAYLAND_DISPLAY=wayland-1\necho SECRET=no\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := RunArgs(a, op)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func TestRunGetsAnEmptyDynaOut(t *testing.T) {
	tmp := t.TempDir()
	a := contract.HostexecRunArgs{Argv: []string{"bash", "-c", `ls -A "$DYNA_OUT" | wc -l; stat -c %a "$DYNA_OUT"; echo hi > "$DYNA_OUT/a.txt"`}, Timeout: 1000}
	out := bashOf(t, a, "op1", "TMPDIR="+tmp)
	if out != "0\n700\n" {
		t.Fatalf("out %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(tmp, "dyna-out-op1", "a.txt")); err != nil || string(b) != "hi\n" {
		t.Fatalf("file %q %v", b, err)
	}
}

func TestDesktopEnvironmentOnlyWhenAsked(t *testing.T) {
	a := contract.HostexecRunArgs{Argv: []string{"bash", "-c", `echo "$DISPLAY|$WAYLAND_DISPLAY|$SECRET"`}, Timeout: 1000}
	if out := bashOf(t, a, "op2", "DISPLAY=:0"); out != ":0||\n" {
		t.Fatalf("plain run: %q", out)
	}
	a.Desktop = true
	if out := bashOf(t, a, "op3", "DISPLAY=:0"); out != ":7|wayland-1|\n" {
		t.Fatalf("desktop run: %q", out)
	}
}

// The collect run prints the files as a tar and removes the folder; with no files it prints nothing.
func TestCollectReadsBackAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	run := func(op contract.OpID) []byte {
		args := RunArgs(CollectArgs(contract.HostexecRunArgs{}, op), op+"-out")
		cmd := exec.Command(args[0], args[1:]...)
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"), 0o755)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + tmp}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	dir := filepath.Join(tmp, "dyna-out-a")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "sub", "s.png"), []byte("png"), 0o600)
	if out := run("a"); !strings.Contains(string(out), "sub/s.png") {
		t.Fatalf("tar %q", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder stays: %v", err)
	}
	os.MkdirAll(filepath.Join(tmp, "dyna-out-b"), 0o700)
	if out := run("b"); len(out) != 0 {
		t.Fatalf("empty folder gave %q", out)
	}
}

// A collect run's stderr stays apart from its stdout, so a tar warning cannot corrupt the archive.
func TestCollectRunKeepsStderrApart(t *testing.T) {
	a := CollectArgs(contract.HostexecRunArgs{}, "op1")
	a.Argv = []string{"bash", "-c", "echo data; echo warn >&2"}
	if out := strings.TrimSpace(bashOfStdout(t, a)); out != "data" {
		t.Fatalf("out %q", out)
	}
}

func bashOfStdout(t *testing.T, a contract.HostexecRunArgs) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := RunArgs(a, "op1")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir()}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// expandingSystemdRun behaves like systemd 254 and later: it expands ${NAME} inside a word, and $NAME when it is the whole word, then runs what follows "--".
// An invalid name inside ${...} becomes empty, as systemd does with a warning.
const expandingSystemdRun = `#!/bin/bash
while [ "$1" != -- ]; do shift; done; shift
out=()
for w in "$@"; do
  while [[ $w =~ \$\{([^}]*)\} ]]; do
    m=${BASH_REMATCH[0]}; n=${BASH_REMATCH[1]}; v=
    [[ $n =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] && v=${!n}
    w=${w/"$m"/$v}
  done
  if [[ $w =~ ^\$([A-Za-z_][A-Za-z0-9_]*)$ ]]; then n=${BASH_REMATCH[1]}; w=${!n}; fi
  out+=("$w")
done
exec "${out[@]}"
`

// The run hands the user's text to the command as written: systemd's expansion of the words after "--" must not touch it.
func TestCommandIsNotExpandedBySystemd(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(expandingSystemdRun), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "x=abc/def; echo \"${x##*/}\" '${n}' 50%s\ncat <<'EOF'\n`${p}` $HOME\nEOF\nprintf '<%s>' \"$@\"; echo"
	cases := map[string]contract.HostexecRunArgs{
		"command":    {Argv: []string{"bash", "-c", script, "arg", "$HOME", "${HOME}", "$$", "a b", ""}, Timeout: 1000},
		"whole word": {Argv: []string{"printf", "<%s>", "$HOME", "${HOME}"}, Timeout: 1000},
	}
	wants := map[string]string{
		"command":    "def ${n} 50%s\n`${p}` $HOME\n<$HOME><${HOME}><$$><a b><>\n",
		"whole word": "<$HOME><${HOME}>",
	}
	for name, a := range cases {
		args := RunArgs(a, "op9")
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "HOME=/home/x"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		if string(out) != wants[name] {
			t.Errorf("%s: got %q, want %q", name, out, wants[name])
		}
	}
}

// With the host's real systemd-run, the command still arrives as written, and the shim and $DYNA_OUT work. It skips where no user manager runs.
func TestRealSystemdRunLeavesCommandAsWritten(t *testing.T) {
	if err := exec.Command("systemd-run", "--user", "--scope", "--quiet", "true").Run(); err != nil {
		t.Skipf("no systemd-run --user here: %v", err)
	}
	a := contract.HostexecRunArgs{Argv: []string{"bash", "-c", `x=abc/def; echo "${x##*/}"; printf '<%s>' "$1" "$2"; echo; echo "$DYNA_ARGV|$DYNA_OUT" | grep -c dyna-out; command -v podman`, "arg", "$HOME", "${HOME}"}, Timeout: 10000}
	args := RunArgs(a, "real1")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "def\n<$HOME><${HOME}>\n1\n") || !strings.Contains(string(out), "dyna-out-real1.bin/podman") {
		t.Fatalf("out %q", out)
	}
}

// QuotedLen matches bash's printf %q for ASCII, so the size the refusal states is the size the run would have had.
func TestQuotedLenMatchesBash(t *testing.T) {
	words := []string{"", "plain", "a b", "#x", "x#", "~y", "y~", "it's", "a\nb", "tab\t", "esc\x1b", "bell\a\x01\x7f", `back\slash`, "${x##*/}", "=+:@%,", "naïve"}
	for c := byte(1); c < 127; c++ {
		words = append(words, "a"+string(rune(c))+"b")
	}
	for _, w := range words {
		out, err := exec.Command("bash", "-c", `printf '%q ' "$1"`, "q", w).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := QuotedLen([]string{w}); got < len(out) || (w != "naïve" && got != len(out)) {
			t.Errorf("%q: QuotedLen %d, bash %d (%s)", w, got, len(out), out)
		}
	}
}
