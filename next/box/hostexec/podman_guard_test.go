package hostexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// fakePodman answers inspect with a label line per name (box-* carry Dyna labels), lists two containers for ps, and logs the
// commands it really ran, so a test sees what reached the real podman.
const fakePodman = `#!/bin/bash
case "$1 $2" in
"container inspect")
	shift 4
	for n; do case $n in box-*) echo "$n dyna.box=${n#box-} dyna.layout=1 other=x";; *) echo "$n other=x";; esac; done;;
"ps -a"|"ps -aq") printf 'box-one\nmine\n';;
*) echo "$*" >> "$LOG";;
esac
`

// guarded runs command in a host-exec wrapper, whose PATH has the shim ahead of the fake podman, and returns its output,
// exit code and the commands the fake podman ran.
func guarded(t *testing.T, command string, env ...string) (string, int, string) {
	t.Helper()
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
	for name, body := range map[string]string{
		"systemd-run": "#!/bin/bash\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n",
		"podman":      fakePodman,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := RunArgs(contract.HostexecRunArgs{Argv: []string{"bash", "-c", command}, Timeout: 1000}, "g")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "LOG=" + log}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	ran, _ := os.ReadFile(log)
	return string(out), code, string(ran)
}

func TestPodmanGuard(t *testing.T) {
	force := "DYNA_FORCE=1"
	for _, c := range []struct {
		name, cmd, env string
		refused        bool
		ran            string
		said           string
	}{
		{name: "labeled target refused", cmd: "podman rm -f box-one", refused: true, said: "box-one dyna.box=one"},
		{name: "refusal names the way to force", cmd: "podman stop box-one", refused: true, said: "DYNA_FORCE=1 podman rm -f <name>"},
		{name: "forced named target passes", cmd: "podman rm -f box-one", env: force, ran: "rm -f box-one\n"},
		{name: "unlabeled target passes", cmd: "podman rm -f mine", ran: "rm -f mine\n"},
		{name: "one labeled among many refused", cmd: "podman kill -s KILL mine box-one", refused: true},
		{name: "container subcommand refused", cmd: "podman --cgroup-manager=systemd container rm box-one", refused: true},
		{name: "prune refused", cmd: "podman system prune -af", refused: true},
		{name: "prune refused when forced", cmd: "podman system prune -af", env: force, refused: true},
		{name: "volume prune refused when forced", cmd: "podman volume prune", env: force, refused: true},
		{name: "rm -a refused", cmd: "podman rm -a", refused: true, said: "box-one"},
		{name: "rm -fa refused", cmd: "podman rm -fa", refused: true},
		{name: "rm -a refused when forced", cmd: "podman rm -a", env: force, refused: true},
		{name: "bulk xargs form refused", cmd: "podman ps -aq | xargs podman rm -f", refused: true, said: "box-one"},
		{name: "other commands pass", cmd: "podman ps -a", ran: ""},
		{name: "run passes", cmd: "podman run --name mine x", ran: "run --name mine x\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code, ran := guarded(t, c.cmd, strings.Fields(c.env)...)
			if c.refused != (code != 0) || (c.refused && ran != "") {
				t.Fatalf("exit %d, ran %q: %s", code, ran, out)
			}
			if !c.refused && ran != c.ran {
				t.Fatalf("ran %q, want %q: %s", ran, c.ran, out)
			}
			if !strings.Contains(out, c.said) {
				t.Fatalf("output %q lacks %q", out, c.said)
			}
		})
	}
}

// A refusal lists a hit with its labels but not the layout noise, and not the unlabeled targets.
func TestPodmanGuardWording(t *testing.T) {
	out, _, _ := guarded(t, "podman rm mine box-two")
	if !strings.Contains(out, "(box-two dyna.box=two other=x)") || strings.Contains(out, "layout") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(out, "Stopped: this would remove Dyna containers that run other sessions and rooms") || !strings.Contains(out, "Never force a bulk or prune command.") {
		t.Fatalf("%q", out)
	}
}
