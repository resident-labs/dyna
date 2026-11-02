package podman

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func fakeTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A host with no mount must keep the plain folder under it empty: a box there would write where no other host can see.
func TestAFolderOnADownMountIsNotMade(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "room", "shared")
	h, err := New("user@host", t.TempDir(), "host", localShell{path: fakeTools(t, map[string]string{"mountpoint": "exit 1"}) + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	err = h.makeMountFolder(context.Background(), box.Mount{HostPath: folder, BoxPath: "/box/shared", OnMount: "/host/share/rooms"})
	var down box.ShareDown
	if !errors.As(err, &down) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(folder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a folder appeared under the down mount: %v", err)
	}
}

func TestAFolderOnAnUpMountIsMade(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "room", "shared")
	h, err := New("user@host", t.TempDir(), "host", localShell{path: fakeTools(t, map[string]string{"mountpoint": "exit 0"}) + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.makeMountFolder(context.Background(), box.Mount{HostPath: folder, BoxPath: "/box/shared", OnMount: "/host/share/rooms"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		t.Fatalf("stat: %v", err)
	}
}

func TestStartRunRefusesADownMount(t *testing.T) {
	h, err := New("user@host", t.TempDir(), "host", localShell{path: fakeTools(t, map[string]string{"mountpoint": "exit 1", "podman": "echo started a box >&2; exit 1"}) + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	err = h.StartRun(context.Background(), box.RunContainer{ID: "run_1", Image: "image", Command: []string{"true"}, Mounts: []box.Mount{{HostPath: filepath.Join(t.TempDir(), "shared"), BoxPath: "/box/shared", OnMount: "/host/share/rooms"}}})
	var down box.ShareDown
	if !errors.As(err, &down) {
		t.Fatalf("err = %v", err)
	}
}

func TestShareNeedsSaysWhatAHostLacks(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "fuse.conf")
	withFuse := fakeTools(t, map[string]string{"fusermount3": "exit 0", "grep": `exec /usr/bin/grep "$@"`})
	for name, c := range map[string]struct{ path, conf, want string }{
		"no fusermount3":   {path: dir, conf: "user_allow_other\n", want: "fusermount3 is missing: install fuse3"},
		"option off":       {path: withFuse, conf: "# user_allow_other\n", want: "user_allow_other is not set in /etc/fuse.conf"},
		"no file":          {path: withFuse, conf: "", want: "user_allow_other is not set in /etc/fuse.conf"},
		"everything there": {path: withFuse, conf: "mount_max = 1000\n  user_allow_other\n"},
	} {
		t.Run(name, func(t *testing.T) {
			os.Remove(conf)
			if c.conf != "" {
				if err := os.WriteFile(conf, []byte(c.conf), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(strings.ReplaceAll(shareNeeds, "/etc/fuse.conf 2", conf+" 2"), "[ -c /dev/fuse ]", "[ -c /dev/null ]")
			cmd := exec.Command("/bin/bash", "-c", script)
			cmd.Env = []string{"PATH=" + c.path}
			out, err := cmd.CombinedOutput()
			if got := strings.TrimSpace(string(out)); got != c.want || (err != nil) != (c.want != "") {
				t.Fatalf("got %q, err %v, want %q", got, err, c.want)
			}
		})
	}
}

func TestCheckReportsAHostWithoutFUSE(t *testing.T) {
	body := "fusermount3 is missing: install fuse3"
	h, err := New("user@host", t.TempDir(), "host", &runner{body: body, status: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.checkShare(context.Background()); err == nil || err.Error() != body {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureShareSaysWhyItFailed(t *testing.T) {
	body := "the mount did not come up\ndetail"
	h, err := New("user@host", t.TempDir(), "host", &runner{body: body, status: 1})
	if err != nil {
		t.Fatal(err)
	}
	var down box.ShareDown
	if err := h.EnsureShare(context.Background(), box.Layout{HostData: "/host"}); !errors.As(err, &down) || down.Reason != body {
		t.Fatalf("err = %v", err)
	}
}

func TestHeartbeatSaysWhetherTheMountIsUp(t *testing.T) {
	r := &seqRunner{bodies: []string{"dyna-share up\ndyna-load 1 1\n", "dyna-load 1 2\n"}, drained: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	reports := make(chan bool)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(_ bool, load Load) { reports <- load.ShareUp }, func(string, contract.PressureKind) {})
	}()
	for _, want := range []bool{true, false} {
		ticks <- time.Now()
		if got := <-reports; got != want {
			t.Fatalf("up = %v, want %v", got, want)
		}
	}
	cancel()
	<-done
}

func TestHeartbeatScriptSeesTheMount(t *testing.T) {
	for name, want := range map[string]bool{"exit 0": true, "exit 1": false} {
		script := `df() { return 1; }; systemctl() { :; }; ` + heartbeatScript
		cmd := exec.Command("bash", "-c", script, "dyna", "/missing", "/mount")
		cmd.Env = []string{"PATH=" + fakeTools(t, map[string]string{"mountpoint": name}) + ":" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		if err != nil || strings.Contains(string(out), shareUpLine) != want {
			t.Fatalf("%s: %s: %v", name, out, err)
		}
	}
}

// The script stays quiet on a host that has its mount, because every box start runs it.
func TestEnsureShareLeavesARunningMountAlone(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	tools := fakeTools(t, map[string]string{"mountpoint": "exit 0", "fusermount3": "echo fusermount3 >> " + log, "systemd-run": "echo systemd-run >> " + log, "systemctl": "exit 1"})
	script := strings.ReplaceAll(strings.ReplaceAll(shareScript, "/etc/fuse.conf 2", os.DevNull+" 2"), "grep -Eq '^[[:space:]]*user_allow_other'", "true")
	script = strings.ReplaceAll(script, "[ -c /dev/fuse ]", "[ -c /dev/null ]")
	cmd := exec.Command("/bin/bash", "-c", script, "dyna-share", filepath.Join(dir, "mount"), filepath.Join(dir, "cache"), "/bin/dyna", "/sock")
	cmd.Env = []string{"PATH=" + tools + ":" + os.Getenv("PATH")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a running mount was touched")
	}
}

func TestShareServerStartsOnlyWhenItIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	state := filepath.Join(dir, "up")
	t.Setenv("PATH", fakeTools(t, map[string]string{
		"systemctl":   "[ -e " + state + " ]",
		"systemd-run": `echo "$*" >> ` + log + "\ntouch " + state,
	})+":"+os.Getenv("PATH"))
	for range 2 {
		if err := EnsureShareServer(context.Background(), "/bin/dyna", "/data/rooms", "/run/share.sock"); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(calls), "share serve --rooms /data/rooms --listen /run/share.sock"); got != 1 {
		t.Fatalf("started %d times: %s", got, calls)
	}
}

func TestEnsureShareStartsAMountWhereThereIsNone(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	state := filepath.Join(dir, "up")
	tools := fakeTools(t, map[string]string{
		"mountpoint":  "[ -e " + state + " ]",
		"fusermount3": "echo fusermount3 >> " + log,
		"systemd-run": `echo "systemd-run $*" >> ` + log + "\ntouch " + state,
		"systemctl":   "exit 3",
	})
	script := strings.ReplaceAll(shareScript, "grep -Eq '^[[:space:]]*user_allow_other'", "true")
	script = strings.ReplaceAll(script, "[ -c /dev/fuse ]", "[ -c /dev/null ]")
	cmd := exec.Command("/bin/bash", "-c", script, "dyna-share", filepath.Join(dir, "mount"), filepath.Join(dir, "cache"), "/bin/dyna", "/sock")
	cmd.Env = []string{"PATH=" + tools + ":" + os.Getenv("PATH")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "share mount --server /sock --dir " + filepath.Join(dir, "mount") + " --cache " + filepath.Join(dir, "cache")
	if !strings.HasPrefix(string(calls), "fusermount3\n") || !strings.Contains(string(calls), want) {
		t.Fatalf("calls: %s", calls)
	}
}
