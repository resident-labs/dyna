package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dyna-agent/internal/userservices"
)

func TestInstallScriptSeedsTemplatesAndNeverTouchesPersonalSkills(t *testing.T) {
	body, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if !strings.Contains(script, `"$BIN" templates seed`) {
		t.Fatal("install.sh does not seed workflow templates")
	}
	if strings.Contains(script, "skill install") {
		t.Fatal("install.sh must not write skills into personal harness folders")
	}
}

func TestRemoteInstallerVerifiesChecksumAndReplacesAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer is for Unix hosts")
	}
	root := t.TempDir()
	scriptBody, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "remote-install.sh")
	writeTestExecutable(t, script, scriptBody)

	fixture := filepath.Join(root, "release-dyna")
	releaseBody := []byte("#!/usr/bin/env bash\nprintf 'dyna release fixture\\n'\n")
	writeTestExecutable(t, fixture, releaseBody)
	sum := sha256.Sum256(releaseBody)
	asset := "dyna_" + runtime.GOOS + "_" + runtime.GOARCH
	checksums := filepath.Join(root, "checksums.txt")
	if err := os.WriteFile(checksums, []byte(hex.EncodeToString(sum[:])+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	uname := `#!/usr/bin/env bash
case "$1" in
  -s) printf '` + installerUnameOS(runtime.GOOS) + `\n' ;;
  -m) printf '` + installerUnameArch(runtime.GOARCH) + `\n' ;;
  *) exit 2 ;;
esac
`
	writeTestExecutable(t, filepath.Join(fakeBin, "uname"), []byte(uname))
	curl := `#!/usr/bin/env bash
set -euo pipefail
url=""
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */checksums.txt) cp "$FIXTURE_CHECKSUMS" "$out" ;;
  */dyna_*) cp "$FIXTURE_BINARY" "$out" ;;
  *) exit 22 ;;
esac
`
	writeTestExecutable(t, filepath.Join(fakeBin, "curl"), []byte(curl))

	installDir := filepath.Join(root, "bin")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "dyna")
	oldBody := []byte("old installed binary\n")
	writeTestExecutable(t, target, oldBody)
	oldHandle, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer oldHandle.Close()

	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = installerTestEnv(t, fakeBin,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+root,
		"DYNA_INSTALL_DIR="+installDir,
		"FIXTURE_BINARY="+fixture,
		"FIXTURE_CHECKSUMS="+checksums,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "verified sha256") {
		t.Fatalf("installer output did not confirm verification:\n%s", out)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(releaseBody) {
		t.Fatalf("installed body = %q, want %q", installed, releaseBody)
	}
	marker, err := os.ReadFile(filepath.Join(installDir, ".dyna-installed"))
	if err != nil || string(marker) != target+"\n" {
		t.Fatalf("install marker = %q, %v", marker, err)
	}
	stillOld, err := io.ReadAll(oldHandle)
	if err != nil {
		t.Fatal(err)
	}
	if string(stillOld) != string(oldBody) {
		t.Fatalf("open old inode changed: got %q, want %q", stillOld, oldBody)
	}
}

func TestRemoteInstallerRejectsChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer is for Unix hosts")
	}
	// The updater package exercises the same mismatch invariant in detail; this
	// focused shell check proves install.sh fails before activating its stage.
	root := t.TempDir()
	scriptBody, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "remote-install.sh")
	writeTestExecutable(t, script, scriptBody)
	fixture := filepath.Join(root, "release-dyna")
	writeTestExecutable(t, fixture, []byte("#!/usr/bin/env bash\necho fixture\n"))
	checksums := filepath.Join(root, "checksums.txt")
	asset := "dyna_" + runtime.GOOS + "_" + runtime.GOARCH
	if err := os.WriteFile(checksums, []byte(strings.Repeat("0", 64)+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, filepath.Join(fakeBin, "uname"), []byte("#!/usr/bin/env bash\n[ \"$1\" = -s ] && echo "+installerUnameOS(runtime.GOOS)+" || echo "+installerUnameArch(runtime.GOARCH)+"\n"))
	writeTestExecutable(t, filepath.Join(fakeBin, "curl"), []byte("#!/usr/bin/env bash\nset -eu\nout=\"\"; url=\"\"\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=\"$2\"; shift 2;; -*) shift;; *) url=\"$1\"; shift;; esac; done\ncase \"$url\" in */checksums.txt) cp \"$FIXTURE_CHECKSUMS\" \"$out\";; *) cp \"$FIXTURE_BINARY\" \"$out\";; esac\n"))
	installDir := filepath.Join(root, "bin")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "dyna")
	want := []byte("keep me\n")
	writeTestExecutable(t, target, want)
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = installerTestEnv(t, fakeBin,
		"PATH="+fakeBin+":"+os.Getenv("PATH"), "HOME="+root,
		"DYNA_INSTALL_DIR="+installDir,
		"FIXTURE_BINARY="+fixture, "FIXTURE_CHECKSUMS="+checksums,
	)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "checksum mismatch") {
		t.Fatalf("installer error = %v, output:\n%s", err, out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(want) {
		t.Fatalf("checksum mismatch replaced target: got %q want %q", got, want)
	}
}

func writeTestExecutable(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
}

func installerUnameOS(goos string) string {
	switch goos {
	case "darwin":
		return "Darwin"
	default:
		return "Linux"
	}
}

func installerUnameArch(goarch string) string {
	if goarch == "arm64" {
		return "arm64"
	}
	return "x86_64"
}

func TestInstallerDelegatesServiceInstallation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux user services")
	}
	for _, tc := range []struct {
		name, dir, target  string
		supported, refused bool
	}{
		{name: "engine unavailable", dir: "custom bin"},
		{name: "engine available", dir: "custom %Z %h $name bin", supported: true},
		{name: "cmd/dyna layout", dir: "custom bin", target: "./cmd/dyna", supported: true},
		{name: "dollar syntax", dir: "custom $name ${name} bin", supported: true},
		{name: "double quote refused", dir: "custom \" bin", supported: true, refused: true},
		{name: "single quote refused", dir: "custom ' bin", supported: true, refused: true},
		{name: "backslash refused", dir: "custom \\ bin", supported: true, refused: true},
		{name: "newline refused", dir: "custom\nbin", supported: true, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			supported := tc.supported
			target := tc.target
			if target == "" {
				target = "."
			}
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			tools := filepath.Join(root, "tools")
			installDir := filepath.Join(root, tc.dir)
			unitDir := filepath.Join(root, "config", "systemd", "user")
			for _, dir := range []string{repo, tools, unitDir, filepath.Join(repo, "web")} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(repo, target), 0755); err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile("../../install.sh")
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(repo, "install.sh")
			if err := os.WriteFile(script, source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module dyna-agent\n"), 0600); err != nil {
				t.Fatal(err)
			}
			oldBinary := filepath.Join(installDir, "dyna")
			if tc.refused {
				if err := os.MkdirAll(installDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldBinary, []byte("keep binary\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldUnit := filepath.Join(unitDir, "dyna-serve.service")
			oldText := "[Service]\nExecStart=/bin/dyna-desktop serve --addr 127.0.0.1:9000 --route https://example.test,https://cards.test\n"
			if err := os.WriteFile(oldUnit, []byte(oldText), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "commands")
			fixture := filepath.Join(root, "fixture-dyna")
			writeTestExecutable(t, fixture, []byte("#!/bin/sh\nprintf 'dyna %s\\n' \"$*\" >> \"$CHECK_LOG\"\nif [ \"$*\" = 'engine install-services' ]; then\n  if [ -f \"$INSTALL_MARKER\" ]; then printf 'install marker present\n' >> \"$CHECK_LOG\"; else printf 'install marker missing\n' >> \"$CHECK_LOG\"; fi\nfi\nif [ \"$*\" = 'engine run --help' ] && [ \"$NO_ENGINE\" = 1 ]; then exit 1; fi\n"))
			writeTestExecutable(t, filepath.Join(tools, "npm"), []byte("#!/bin/sh\nexit 0\n"))
			writeTestExecutable(t, filepath.Join(tools, "go"), []byte("#!/bin/sh\nfor arg; do target=$arg; done\n[ \"$target\" = \"$BUILD_TARGET\" ] || exit 1\nwhile [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then cp \"$FIXTURE_BINARY\" \"$2\"; exit; fi; shift; done\nexit 1\n"))
			writeTestExecutable(t, filepath.Join(tools, "systemctl"), []byte("#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$CHECK_LOG\"\n"))
			noEngine := "0"
			if !supported {
				noEngine = "1"
			}
			cmd := exec.Command("bash", script)
			cmd.Env = installerTestEnv(t, tools, "PATH="+tools+":"+os.Getenv("PATH"), "HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "DYNA_INSTALL_DIR="+installDir, "INSTALL_MARKER="+filepath.Join(installDir, ".dyna-installed"), "CHECK_LOG="+log, "FIXTURE_BINARY="+fixture, "NO_ENGINE="+noEngine, "BUILD_TARGET=./cmd/dyna")
			out, err := cmd.CombinedOutput()
			commands, _ := os.ReadFile(log)
			if tc.refused {
				if err == nil || !strings.Contains(string(out), "install directory must not contain quotes, backslashes or newlines") {
					t.Fatalf("invalid directory was not refused: %v\n%s", err, out)
				}
				if body, readErr := os.ReadFile(oldUnit); readErr != nil || string(body) != oldText {
					t.Fatalf("old unit changed: %q %v", body, readErr)
				}
				if len(commands) != 0 {
					t.Fatalf("ran commands for refused directory: %s", commands)
				}
				if body, readErr := os.ReadFile(oldBinary); readErr != nil || string(body) != "keep binary\n" {
					t.Fatalf("installed binary changed: %q %v", body, readErr)
				}
				return
			}
			if !supported {
				if err == nil {
					t.Fatalf("installer accepted a binary without engine support:\n%s", out)
				}
				if body, readErr := os.ReadFile(oldUnit); readErr != nil || string(body) != oldText {
					t.Fatalf("old unit changed: %q %v", body, readErr)
				}
				if strings.Contains(string(commands), "systemctl") {
					t.Fatalf("changed services before engine check: %s", commands)
				}
				return
			}
			if err != nil {
				t.Fatalf("installer failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(commands), "install marker present") {
				t.Fatalf("engine service activated before the install marker was written:\n%s", commands)
			}
			previous := -1
			for _, command := range []string{"dyna engine run --help", "dyna engine install-services"} {
				index := strings.Index(string(commands), command)
				if index < 0 || index <= previous {
					t.Fatalf("missing or out-of-order %q:\n%s", command, commands)
				}
				previous = index
			}
			if strings.Contains(string(commands), "systemctl") {
				t.Fatalf("install.sh bypassed the shared service installer: %s", commands)
			}
			// The fixture binary only logs the delegation. Exercise the shared
			// writer separately to verify the executable path and offline parser.
			if err := userservices.Write(userservices.Options{Binary: filepath.Join(installDir, "dyna"), Dir: unitDir}); err != nil {
				t.Fatal(err)
			}
			unit, err := os.ReadFile(filepath.Join(unitDir, "dyna-engine.service"))
			if err != nil {
				t.Fatal(err)
			}
			escaped := strings.ReplaceAll(filepath.Join(installDir, "dyna"), "%", "%%")
			want := "ExecStart=\"" + escaped + "\" engine run"
			if !strings.Contains(string(unit), want) {
				t.Fatalf("unit has wrong executable:\n%s", unit)
			}
			t.Run("offline systemd parser", func(t *testing.T) {
				analyze, err := exec.LookPath("systemd-analyze")
				if err != nil {
					t.Skip("systemd-analyze unavailable")
				}
				parserUnit := filepath.Join(root, "parser.service")
				var executableLine string
				for _, line := range strings.Split(string(unit), "\n") {
					if strings.HasPrefix(line, "ExecStart=") {
						executableLine = line
					}
				}
				if err := os.WriteFile(parserUnit, []byte("[Service]\n"+executableLine+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				verify := exec.Command(analyze, "verify", "--man=no", "--generators=no", parserUnit)
				verify.Env = append(installerTestEnv(t, tools), "SYSTEMD_OFFLINE=1")
				if raw, err := verify.CombinedOutput(); err != nil {
					t.Fatalf("unit parser rejected executable path: %v\n%s", err, raw)
				}
			})
		})
	}
}

// installerTestEnv confines both files and service-manager access. The fake
// systemctl is the first PATH entry; even an absolute systemctl call has no
// route to the real user manager through the environment.
func installerTestEnv(t *testing.T, fakeBin string, extra ...string) []string {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	fake := []byte("#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$CHECK_LOG\"\n")
	writeTestExecutable(t, filepath.Join(fakeBin, "systemctl"), fake)
	env := []string{}
	for _, pair := range withoutDynaEnv(os.Environ()) {
		key, _, _ := strings.Cut(pair, "=")
		switch key {
		case "HOME", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS":
			continue
		}
		if strings.HasPrefix(key, "SYSTEMD_") {
			continue
		}
		env = append(env, pair)
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "PATH="+fakeBin+":"+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_RUNTIME_DIR="+runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "no-session-bus"),
		"DBUS_SYSTEM_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "no-system-bus"),
		"CHECK_LOG="+filepath.Join(root, "systemctl.log"))
	env = append(env, extra...)
	if err := guardInstallerTestEnv(env, fakeBin); err != nil {
		t.Fatal(err)
	}
	return env
}

func guardInstallerTestEnv(env []string, fakeBin string) error {
	values := map[string]string{}
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	first := strings.Split(values["PATH"], string(os.PathListSeparator))[0]
	if first != fakeBin {
		return fmt.Errorf("fake systemctl is not first on PATH")
	}
	executable := filepath.Join(fakeBin, "systemctl")
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("systemctl stub is not a regular executable")
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		value := values[key]
		if !filepath.IsAbs(value) || value == os.Getenv(key) || value == filepath.Join(os.Getenv("HOME"), ".config") || strings.Contains(value, "/run/user/") {
			return fmt.Errorf("%s can reach the real user environment", key)
		}
	}
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS"} {
		address := values[key]
		path, ok := strings.CutPrefix(address, "unix:path="+values["XDG_RUNTIME_DIR"]+string(os.PathSeparator))
		if !ok || path == "" || strings.Contains(path, "/") || strings.Contains(address, "/run/user/") {
			return fmt.Errorf("%s can reach a real service manager", key)
		}
		target := filepath.Join(values["XDG_RUNTIME_DIR"], path)
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return fmt.Errorf("%s points at an existing socket", key)
		}
	}
	if _, err := os.Lstat(filepath.Join(values["XDG_RUNTIME_DIR"], "systemd")); !os.IsNotExist(err) {
		return fmt.Errorf("test runtime can reach a user systemd manager")
	}
	return nil
}

func TestInstallerEnvironmentRejectsLiveManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer environment")
	}
	fakeBin := t.TempDir()
	safe := installerTestEnv(t, fakeBin)
	for _, entry := range []string{
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"XDG_CONFIG_HOME=" + os.Getenv("HOME") + "/.config",
		"PATH=/usr/bin",
	} {
		candidate := append(append([]string{}, safe...), entry)
		if err := guardInstallerTestEnv(candidate, fakeBin); err == nil {
			t.Errorf("accepted live-manager route %q", entry)
		}
	}
}
