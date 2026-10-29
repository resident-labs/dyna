package computers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shellquote"
)

func Enabled() (bool, string) {
	if os.Getenv("DYNA_COMPUTERS_ENABLED") == "0" {
		return false, "DYNA_COMPUTERS_ENABLED is 0"
	}
	if testing.Testing() && os.Getenv("DYNA_COMPUTERS_TEST_ENABLE") != "1" {
		return false, "computers are disabled in Go tests"
	}
	if runtime.GOOS != "linux" {
		return false, "computers require Linux"
	}
	v, err := settings.Load("")
	if err != nil {
		return false, err.Error()
	}
	if !v.ComputersEnabled {
		return false, "computers.enabled is false"
	}
	if _, err := exec.LookPath("labwc"); err != nil {
		return false, "labwc is not installed"
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return false, "XDG_RUNTIME_DIR is not set"
	}
	return true, ""
}
func ReadMeta(id string) (m Meta, err error) {
	if err = ValidateID(id); err != nil {
		return
	}
	ok, err := readRecord(id, metaRecord, &m)
	if err == nil && !ok {
		err = fs.ErrNotExist
	}
	return
}
func publicName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "dyna-computer-" + hex.EncodeToString(sum[:8])
}

// ActorID is the computer ID of an acting identity (subagent.Actor): a
// session's own ID, or for root (no session) one stable computer per room.
func ActorID(session, roomID string) string {
	if session != "" {
		return session
	}
	return "root-" + roomID
}

// Ensure registers public endpoints with the shared daemon. It never starts labwc.
func Ensure(ctx context.Context, id, room string) (Meta, error) {
	if err := ValidateID(id); err != nil {
		return Meta{}, err
	}
	if ok, why := Enabled(); !ok {
		return Meta{}, fmt.Errorf("%s", why)
	}
	if m, err := NewClient(id).Register(ctx, room); err == nil {
		clearFailedSetup(id)
		return m, nil
	}
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Meta{}, err
	}
	if err = startDaemon(ctx, exe); err != nil {
		return Meta{}, err
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return Meta{}, ctx.Err()
		case <-deadline.C:
			return Meta{}, fmt.Errorf("computer daemon did not start; inspect %s", filepath.Join(Root(), "daemon.log"))
		case <-tick.C:
			if m, err := NewClient(id).Register(ctx, room); err == nil {
				clearFailedSetup(id)
				return m, nil
			}
		}
	}
}

// runningCheckTimeout bounds the status read that decides whether a computer
// is running. It is short because a shell start must not wait on a daemon.
const runningCheckTimeout = 300 * time.Millisecond

// RunningEnvironment returns the display environment of a computer that is
// already running, and false when none runs. It never registers, creates or
// starts a computer: a shell that starts on its own must not start a desktop.
func RunningEnvironment(ctx context.Context, id string, base []string) ([]string, bool) {
	if ok, _ := Enabled(); !ok {
		return nil, false
	}
	call, cancel := context.WithTimeout(ctx, runningCheckTimeout)
	defer cancel()
	st, err := GetStatus(call, id)
	if err != nil || st.State != StateRunning {
		return nil, false
	}
	m, err := ReadMeta(id)
	if err != nil {
		return nil, false
	}
	// The status this path already read carries the running desktop's GPU
	// variables, so the environment cannot disagree with the desktop.
	env, err := buildEnvironment(m, base, gpuEnvForStatus(st))
	if err != nil {
		return nil, false
	}
	return env, true
}

// ShimDir is the directory of a computer's browser shims.
func ShimDir(id string) string { return filepath.Join(Dir(id), "bin") }

func Environment(m Meta, base []string) ([]string, error) {
	// Apps follow the running desktop's GPU, whatever the setting says now;
	// with no desktop, the next start's choice applies (D-352 review).
	return buildEnvironment(m, base, appGPUEnv(m.SessionID))
}

// buildEnvironment is Environment for known GPU variables. The daemon calls
// it with the desktop's own values, so it never asks itself for status.
func buildEnvironment(m Meta, base []string, gpuEnv map[string]string) ([]string, error) {
	shim, err := browserShims(m.SessionID, base)
	if err != nil {
		return nil, err
	}
	ownerWayland := envValue(base, "DYNA_OWNER_WAYLAND_DISPLAY")
	if ownerWayland == "" {
		ownerWayland = envValue(base, "WAYLAND_DISPLAY")
	}
	ownerDisplay := envValue(base, "DYNA_OWNER_DISPLAY")
	if ownerDisplay == "" {
		ownerDisplay = envValue(base, "DISPLAY")
	}
	// Save the bus the app had, so a shell that keeps the owner display gets the
	// owner's bus back. A bus saved by an earlier injection stays.
	ownerBus, saved := envLookup(base, OwnerBusKey)
	if !saved {
		ownerBus = envValue(base, "DBUS_SESSION_BUS_ADDRESS")
	}
	env := mergeEnvironment(base, map[string]string{OwnerBusKey: ownerBus, "DYNA_OWNER_WAYLAND_DISPLAY": ownerWayland, "DYNA_OWNER_DISPLAY": ownerDisplay, "WAYLAND_DISPLAY": m.WaylandDisplay, "DISPLAY": m.Display, "XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "labwc", "PATH": shim + string(os.PathListSeparator) + envValue(base, "PATH")})
	// Apps in the computer render on the given node, off the owner's display
	// GPU by default (D-352).
	env = mergeGPUEnvironment(env, gpuEnv)
	// Every launch path that builds an app's environment here (open, run, env,
	// the browser) points it at the computer's own session bus.
	env = mergeEnvironment(env, privateBusEnvironment(m.SessionID))
	return browserEnvironment(m, env)
}

// Inject selects the Dyna session stamp from the actual harness environment.
// A disabled computer leaves every environment variable unchanged.
func Inject(ctx context.Context, base []string) ([]string, error) {
	if ok, _ := Enabled(); !ok {
		return base, nil
	}
	id := envValue(base, "DYNA_SESSION")
	if id == "" {
		return base, nil
	}
	room := envValue(base, "DYNA_ROOM_ID")
	if room == "" {
		room = envValue(base, "DYNA_ROOM")
	}
	m, err := Ensure(ctx, id, room)
	if err != nil {
		return nil, err
	}
	return Environment(m, base)
}

// OwnerBusKey holds the session bus address an environment had before
// Environment pointed it at the computer's bus.
const OwnerBusKey = "DYNA_OWNER_DBUS_SESSION_BUS_ADDRESS"

func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, line := range env {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, line := range env {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// BusSocket is the computer's own D-Bus session bus socket.
func BusSocket(id string) string { return filepath.Join(RuntimeDir(id), "bus") }

// privateBusEnvironment names the computer's own session bus for an app, so its
// portal requests, such as file choosers, stay inside the computer. The address
// is fixed and never depends on the bus existing yet: the desktop starts lazily,
// and an app that got the host's bus address while the computer was still
// stopped drew its file chooser on the host display (D-274). If the computer has
// no bus (no dbus-daemon), the app finds no bus, never the host's.
func privateBusEnvironment(id string) map[string]string {
	return map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + BusSocket(id)}
}

// engineEnvironment is the engine's own environment for a computer process the
// engine starts. It never carries the engine's secrets. A harness launch is not
// one of these: its environment holds the session's own credentials.
func engineEnvironment() []string {
	return processguard.WithoutSecrets(os.Environ())
}

// mergeEnvironment builds a computer launch environment from base with values set.
func mergeEnvironment(base []string, values map[string]string) []string {
	out := make([]string, 0, len(base)+len(values))
	for _, s := range base {
		k, _, _ := strings.Cut(s, "=")
		if _, ok := values[k]; !ok {
			out = append(out, s)
		}
	}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}
func browserShims(id string, env []string) (string, error) {
	dir := ShimDir(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := prepareChromium(id); err != nil {
		return "", err
	}
	if err := os.WriteFile(chromiumWrapper(id), []byte(chromiumScript(id)), 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"chromium", "google-chrome", "google-chrome-stable", "brave", "firefox", "hypruse"} {
		profile := filepath.Join(Dir(id), "profiles", name)
		if err := os.MkdirAll(profile, 0700); err != nil {
			return "", err
		}
		args := " --user-data-dir=" + shellquote.Quote(profile)
		if name == "firefox" {
			args = " --no-remote --profile " + shellquote.Quote(profile)
		}
		ownerDisplays := ""
		if name == "hypruse" {
			args = ""
			ownerDisplays = "if [ -n \"$DYNA_OWNER_WAYLAND_DISPLAY\" ]; then WAYLAND_DISPLAY=$DYNA_OWNER_WAYLAND_DISPLAY; export WAYLAND_DISPLAY; fi\nif [ -n \"$DYNA_OWNER_DISPLAY\" ]; then DISPLAY=$DYNA_OWNER_DISPLAY; export DISPLAY; fi\n"
		}
		script := "#!/bin/sh\n" + computerPathScript() + ownerDisplays + "exec " + name + args + " \"$@\"\n"
		if name != "firefox" && name != "hypruse" {
			script = chromiumScript(id)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// List reads stopped records as well as live supervisors. Room is optional.
func List(ctx context.Context, room string) ([]Status, error) {
	entries, err := os.ReadDir(Root())
	if os.IsNotExist(err) {
		return []Status{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Status{}
	for _, e := range entries {
		if !e.IsDir() || ValidateID(e.Name()) != nil {
			continue
		}
		m, err := ReadMeta(e.Name())
		if err != nil || room != "" && m.Room != room {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		s, err := NewClient(m.SessionID).Status(call)
		cancel()
		if err != nil {
			_, _ = readRecord(m.SessionID, statusRecord, &s)
			s.SessionID = m.SessionID
			s.Room = m.Room
			if s.State != StateDestroyed {
				s.State = StateIdleStopped
			}
			s.RSSKiB = 0
			s.Stream = Stream{}
			s.StreamRSSKiB = 0
			s.Control = ControlAgent
			s.Viewers = 0
		}
		out = append(out, s)
	}
	return out, nil
}
func savedStatus(id string) Status {
	var s Status
	_, _ = readRecord(id, statusRecord, &s)
	return s
}

// GetStatus reads historical state when the daemon is absent. Read-only calls
// never create a daemon, compositor, or registration.
func GetStatus(ctx context.Context, id string) (Status, error) {
	if err := ValidateID(id); err != nil {
		return Status{}, err
	}
	if s, err := NewClient(id).Status(ctx); err == nil {
		if why, ferr := storedFailure(id); ferr == nil && why != "" {
			s.Reason = why
		}
		if ok, why := Enabled(); !ok {
			s.Reason = why
		}
		return s, nil
	}
	s := savedStatus(id)
	if s.SessionID == "" {
		s = Status{SessionID: id, Apps: []App{}, Control: ControlAgent, Reason: "no desktop yet"}
	} else if s.State != StateDestroyed {
		s.State = StateIdleStopped
	}
	s.RSSKiB = 0
	s.Stream = Stream{}
	s.StreamRSSKiB = 0
	s.Viewers = 0
	s.Control = ControlAgent
	if why, ferr := storedFailure(id); ferr == nil && why != "" {
		s.Reason = why
	}
	if ok, why := Enabled(); !ok {
		s.Reason = why
	}
	return s, nil
}
