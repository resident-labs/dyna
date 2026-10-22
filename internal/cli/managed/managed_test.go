package managed

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	good := options{name: "dyna-test", image: "root.tar.zst", code: "/home/test/my code", port: 7477, cardPort: 7478}
	if err := validate(good); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../other", "a.service", "-bad", "", "a\nb", "x_y", strings.Repeat("x", 64)} {
		o := good
		o.name = name
		if validate(o) == nil {
			t.Errorf("accepted name %q", name)
		}
	}
	for _, code := range []string{"/", "relative", "/tmp/x:y", "/tmp/trailing ", "/tmp/x\ny", "/tmp/%m", "/etc", "/etc/other", "/home/dyna/code", "/run/x"} {
		o := good
		o.code = code
		if validate(o) == nil {
			t.Errorf("accepted code %q", code)
		}
	}
	for _, ports := range [][2]int{{0, 7478}, {7477, 7477}, {7477, 65536}} {
		o := good
		o.port = ports[0]
		o.cardPort = ports[1]
		if validate(o) == nil {
			t.Errorf("accepted ports %v", ports)
		}
	}
	o := good
	o.image = ""
	if validate(o) == nil {
		t.Fatal("accepted missing image")
	}
}
func TestConfig(t *testing.T) {
	o := options{name: "dyna-test", code: "/home/test/my code"}
	for _, dri := range []bool{false, true} {
		got := nspawnConfig(o, dri)
		for _, want := range []string{marker, "Boot=yes", "PrivateUsers=no", "VirtualEthernet=no", `Bind=/home/test/my code:/home/test/my code`} {
			if !strings.Contains(got, want) {
				t.Errorf("config lacks %q: %s", want, got)
			}
		}
		if strings.Contains(got, "Bind=/dev/dri") != dri {
			t.Fatal("incorrect device bind")
		}
		got = dropin(dri)
		for _, want := range []string{"CPUWeight=20", "MemoryHigh=65%", "MemoryMax=80%"} {
			if !strings.Contains(got, want) {
				t.Errorf("drop-in lacks %q", want)
			}
		}
		if strings.Contains(got, "DeviceAllow=char-drm rw") != dri {
			t.Fatal("incorrect device policy")
		}
	}
}
func TestImportCommands(t *testing.T) {
	for _, tool := range []string{"importctl", "machinectl"} {
		want := []string{tool, "import-tar"}
		if tool == "importctl" {
			want = append(want, "--class=machine")
		}
		want = append(want, "/tmp/image.tar.zst", "dyna-test")
		if got := importArgs(tool, "/tmp/image.tar.zst", "dyna-test"); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
func TestStageImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root.tar")
	if err := os.WriteFile(path, []byte("rootfs"), 0600); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := stageImage(context.Background(), path, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got != path {
		t.Fatal(got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed source image")
	}
	for _, source := range []string{"http://example.com/image.tar", "https://user:pass@example.com/root.tar", "ftp://example.com/root.tar", filepath.Dir(path), path + "missing"} {
		_, cleanup, err := stageImage(context.Background(), source, "", io.Discard)
		cleanup()
		if err == nil {
			t.Errorf("accepted %q", source)
		}
	}
}
func TestCommandsRequireManaged(t *testing.T) {
	for _, c := range []*cobra.Command{NewInstallCommand(), NewUninstallCommand()} {
		c.SetArgs(nil)
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "--managed") {
			t.Fatalf("got %v", err)
		}
	}
}

// An argument containing shell syntax must stay one argument and remain inert.
func TestSudoKeepsArgumentsAndInput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	input := filepath.Join(dir, "input")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TEST_ARGS\"\ncat > \"$TEST_INPUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("TEST_ARGS", log)
	t.Setenv("TEST_INPUT", input)
	var out strings.Builder
	r := runner{context.Background(), &out, &out}
	arg := "path with spaces; $(false)"
	if err := r.sudo(strings.NewReader("script\n"), "systemd-nspawn", arg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "systemd-nspawn\n"+arg+"\n" {
		t.Fatalf("arguments changed: %q", b)
	}
	b, err = os.ReadFile(input)
	if err != nil || string(b) != "script\n" {
		t.Fatalf("input = %q, %v", b, err)
	}
	if !strings.HasPrefix(out.String(), "+ sudo ") {
		t.Fatal("missing command notice")
	}
}

func TestImageChecksums(t *testing.T) {
	body := []byte("rootfs")
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	path := filepath.Join(t.TempDir(), "root.tar")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var warning strings.Builder
	_, cleanup, err := stageImage(context.Background(), path, "", &warning)
	cleanup()
	if err != nil || !strings.Contains(warning.String(), "Warning:") {
		t.Fatalf("warning=%q err=%v", warning.String(), err)
	}
	for _, expected := range []string{hash, strings.ToUpper(hash), strings.Repeat("0", 64)} {
		_, cleanup, err := stageImage(context.Background(), path, expected, io.Discard)
		cleanup()
		if (err == nil) != strings.EqualFold(expected, hash) {
			t.Fatalf("hash=%s err=%v", expected, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed local source")
	}
	for _, text := range []string{hash + "  root.tar", hash + " *root.tar\n"} {
		if got, err := parseChecksum(text, "root.tar"); err != nil || got != hash {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	for _, text := range []string{hash, hash + "  wrong.tar", "invalid  root.tar", strings.Repeat("a", 4097)} {
		if _, err := parseChecksum(text, "root.tar"); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	good := options{name: "dyna", image: path, code: "/tmp/code", port: 7467, cardPort: 7468}
	for _, expected := range []string{"short", strings.Repeat("z", 64)} {
		good.sha256 = expected
		if validate(good) == nil {
			t.Fatalf("accepted hash %q", expected)
		}
	}
}

func TestDownloadedImageVerification(t *testing.T) {
	body := "downloaded rootfs"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	checksum := hash + "  root.tar\n"
	checksumStatus := http.StatusOK
	imageRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/root.tar.sha256":
			w.WriteHeader(checksumStatus)
			fmt.Fprint(w, checksum)
		case "/root.tar":
			imageRequests++
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for _, tc := range []struct {
		name, expected, checksum string
		status                   int
		success                  bool
		downloads                int
	}{
		{"published", "", hash + "  root.tar\n", 200, true, 1},
		{"explicit", hash, "bad", 404, true, 1},
		{"mismatch", strings.Repeat("0", 64), "", 200, false, 1},
		{"published mismatch", "", strings.Repeat("0", 64) + "  root.tar", 200, false, 1},
		{"missing", "", "", 404, false, 0},
		{"wrong asset", "", hash + "  other.tar", 200, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checksum, checksumStatus = tc.checksum, tc.status
			before := imageRequests
			path, cleanup, err := stageImage(context.Background(), server.URL+"/root.tar", tc.expected, io.Discard)
			if (err == nil) != tc.success {
				t.Fatalf("path=%s err=%v", path, err)
			}
			if imageRequests-before != tc.downloads {
				t.Fatal("unexpected image download count")
			}
			if tc.success {
				b, err := os.ReadFile(path)
				if err != nil || string(b) != body {
					t.Fatalf("body=%q err=%v", b, err)
				}
			}
			cleanup()
			entries, err := os.ReadDir(temp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("download left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestMachinesTargetOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, initial, deps, machines string
		queryFailure, owned           bool
		disable                       bool
	}{
		{name: "Dyna enabled target", initial: "disabled", deps: "", disable: true},
		{name: "automatic machine store mount", initial: "disabled", deps: "var-lib-machines.mount", disable: true},
		{name: "target already enabled", initial: "enabled", deps: ""},
		{name: "last Dyna machine inherits ownership", initial: "enabled", owned: true, disable: true},
		{name: "another enabled machine", initial: "disabled", deps: "var-lib-machines.mount systemd-nspawn@other.service"},
		{name: "another running machine", initial: "disabled", deps: "", machines: "other container"},
		{name: "dependency query failed", initial: "disabled", queryFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			if tc.owned {
				if err := os.WriteFile(state, []byte("Enabled by Dyna\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(dir, "log")
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("TARGET_STATUS", tc.initial)
			t.Setenv("TARGET_DEPS", tc.deps)
			t.Setenv("MACHINES", tc.machines)
			t.Setenv("COMMAND_LOG", log)
			if tc.queryFailure {
				t.Setenv("QUERY_FAILURE", "yes")
			} else {
				t.Setenv("QUERY_FAILURE", "")
			}
			scripts := map[string]string{
				"systemctl": `#!/bin/sh
case "$1" in
is-enabled) echo "$TARGET_STATUS"; test "$TARGET_STATUS" != disabled ;;
show) test -z "$QUERY_FAILURE" || exit 2; printf '%s\n' "$TARGET_DEPS" ;;
disable) echo disable-target >> "$COMMAND_LOG" ;;
*) exit 2 ;;
esac
`,
				"machinectl": `#!/bin/sh
case "$1" in
enable) echo enable-machine >> "$COMMAND_LOG" ;;
list) printf '%s' "$MACHINES" ;;
*) exit 2 ;;
esac
`,
			}
			for name, script := range scripts {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			run := func(script string) error {
				script = strings.ReplaceAll(script, "/etc/systemd/nspawn/.dyna-machines-target", state)
				script = strings.ReplaceAll(script, "/run/lock/dyna-managed.lock", filepath.Join(dir, "lock"))
				cmd := exec.Command("sh", "-s", "--", "test-machine")
				cmd.Stdin = strings.NewReader(script)
				b, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("%s: %w", b, err)
				}
				return nil
			}
			if err := run(enableMachineScript); err != nil {
				t.Fatal(err)
			}
			if err := run(cleanupTargetScript); (err != nil) != tc.queryFailure {
				t.Fatalf("cleanup: %v", err)
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "disable-target") != tc.disable {
				t.Fatalf("commands=%s", b)
			}
			_, err = os.Stat(state)
			wantState := (tc.initial == "disabled" || tc.owned) && !tc.disable
			if (err == nil) != wantState {
				t.Fatalf("state err=%v want present=%v", err, wantState)
			}
		})
	}
}

func TestInstallRejectsMismatchBeforeImport(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "image.tar")
	if err := os.WriteFile(image, []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "sudo-log")
	for _, tool := range []string{"sudo", "machinectl", "systemd-nspawn", "systemctl", "install", "flock"} {
		script := "#!/bin/sh\nexit 0\n"
		if tool == "sudo" {
			script = "#!/bin/sh\necho called > \"$SUDO_LOG\"\nexit 0\n"
		}
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("SUDO_LOG", log)
	// Get two different ephemeral ports without hard-coding host port numbers.
	a, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	portA, portB := a.Addr().(*net.TCPAddr).Port, b.Addr().(*net.TCPAddr).Port
	a.Close()
	b.Close()
	o := options{name: fmt.Sprintf("dyna-checksum-test-%d", os.Getpid()), image: image, sha256: strings.Repeat("0", 64), code: filepath.Join(dir, "code"), port: portA, cardPort: portB}
	var output strings.Builder
	err = install(runner{context.Background(), &output, &output}, o)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("sudo ran before checksum verification: %v", err)
	}
}
