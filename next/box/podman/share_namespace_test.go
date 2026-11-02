package podman

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// namespaceRig is a host whose mount is up as device 0:89, and whose Podman namespace has the device in ns, or nothing there
// when ns is empty. A container runs while running is set. migrate is what podman system migrate does: "renew" gives Podman a
// new namespace with the host's mounts, "same" leaves the old one, and "fail" fails.
type namespaceRig struct {
	dir, log, migrate string
}

func newNamespaceRig(t *testing.T, ns string, running bool, migrate string) *namespaceRig {
	t.Helper()
	rig := &namespaceRig{dir: t.TempDir(), migrate: migrate}
	rig.log = filepath.Join(rig.dir, "calls")
	write := func(name, text string) {
		if err := os.WriteFile(rig.file(name), []byte(text+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("host", "0:89")
	if ns != "" {
		write("ns", ns)
	}
	if running {
		write("running", "c0ffee")
	}
	return rig
}

func (rig *namespaceRig) file(name string) string { return filepath.Join(rig.dir, name) }

// run runs the whole of shareScript, as EnsureShare does on a host whose mount is already up, and returns its output.
func (rig *namespaceRig) run(t *testing.T) (string, error) {
	t.Helper()
	migrate := map[string]string{
		"renew": "cp " + rig.file("host") + " " + rig.file("ns"),
		"same":  "exit 0",
		"fail":  "echo 'Error: cannot kill the pause process' >&2; exit 125",
	}[rig.migrate]
	tools := fakeTools(t, map[string]string{
		// Under podman unshare, mountpoint reads Podman's namespace instead of the host's.
		"mountpoint": `[ "$1" = -q ] && exit 0
f=` + rig.file("host") + `; [ -n "$IN_PODMAN" ] && f=` + rig.file("ns") + `
[ -e "$f" ] || { echo "mountpoint: not a mount point" >&2; exit 32; }
cat "$f"`,
		"podman": `case "$1" in
unshare) shift; export IN_PODMAN=1; exec "$@" ;;
ps) cat ` + rig.file("running") + ` 2>/dev/null; exit 0 ;;
system) echo "podman $*" >> ` + rig.log + `; ` + migrate + ` ;;
*) echo "podman $*" >> ` + rig.log + `; exit 125 ;;
esac`,
	})
	script := strings.ReplaceAll(shareScript, "grep -Eq '^[[:space:]]*user_allow_other'", "true")
	script = strings.ReplaceAll(script, "[ -c /dev/fuse ]", "[ -c /dev/null ]")
	cmd := exec.Command("/bin/bash", "-c", script, "dyna-share", filepath.Join(rig.dir, "mount"), filepath.Join(rig.dir, "cache"), "/bin/dyna", "/sock")
	cmd.Env = []string{"PATH=" + tools + ":" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (rig *namespaceRig) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(rig.log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// The mount was up before Podman's namespace was made, or the host passes mounts on to it, so nothing is renewed.
func TestShareSeenByPodmanIsLeftAlone(t *testing.T) {
	rig := newNamespaceRig(t, "0:89", true, "renew")
	if out, err := rig.run(t); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if calls := rig.calls(t); calls != "" {
		t.Fatalf("calls: %s", calls)
	}
}

// As on lt-24-005: the mount came up after Podman's namespace, which has only the plain folder there, and every box failed
// with statfs. With no container running, Podman gets a new namespace, and the mount is in it.
func TestShareUnseenByAnIdlePodmanRenewsItsNamespace(t *testing.T) {
	rig := newNamespaceRig(t, "", false, "renew")
	if out, err := rig.run(t); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if calls := rig.calls(t); calls != "podman system migrate\n" {
		t.Fatalf("calls: %q", calls)
	}
}

// A mount that died and came back leaves Podman's namespace holding the dead one, which has its own device.
func TestShareRemountedUnderAnIdlePodmanRenewsItsNamespace(t *testing.T) {
	rig := newNamespaceRig(t, "0:77", false, "renew")
	if out, err := rig.run(t); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if calls := rig.calls(t); calls != "podman system migrate\n" {
		t.Fatalf("calls: %q", calls)
	}
}

// Renewing the namespace stops every running container, so a busy host keeps its boxes and refuses new ones until it is idle.
func TestShareUnseenByABusyPodmanStopsNothing(t *testing.T) {
	rig := newNamespaceRig(t, "", true, "renew")
	out, err := rig.run(t)
	if err == nil || out != "Podman's namespace is older than the mount and cannot see it; it is renewed once no container runs on this host" {
		t.Fatalf("%v: %q", err, out)
	}
	if calls := rig.calls(t); calls != "" {
		t.Fatalf("a busy host was renewed: %s", calls)
	}
}

func TestShareUnseenAfterMigrateSaysWhy(t *testing.T) {
	for migrate, want := range map[string]string{
		"same": "Podman cannot see the mount even in a new namespace",
		"fail": "podman system migrate failed\nError: cannot kill the pause process",
	} {
		t.Run(migrate, func(t *testing.T) {
			rig := newNamespaceRig(t, "", false, migrate)
			if out, err := rig.run(t); err == nil || out != want {
				t.Fatalf("%v: %q", err, out)
			}
		})
	}
}
