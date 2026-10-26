//go:build linux

package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/browserconfig"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
)

// LaunchChromium is the entry point used by every Chromium shim. The launcher
// keeps a lock until Chromium exits, including headless automation sessions.
func LaunchChromium(ctx context.Context, id string, args []string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	// Playwright gives Chromium its debugging pipes on descriptors 3 and 4.
	// Duplicate them before this process opens locks, sockets or log files.
	var pipes []*os.File
	for _, arg := range args {
		if arg == "--remote-debugging-pipe" {
			for _, fd := range []uintptr{3, 4} {
				duplicate, err := unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 10)
				if err != nil {
					for _, f := range pipes {
						_ = f.Close()
					}
					return fmt.Errorf("Chromium debugging pipe %d: %w", fd, err)
				}
				pipes = append(pipes, os.NewFile(uintptr(duplicate), "chromium-debug-pipe"))
			}
			break
		}
	}
	defer func() {
		for _, f := range pipes {
			_ = f.Close()
		}
	}()
	joinBrowserCgroup(id)
	lock, err := browserFileLock(ctx, filepath.Join(Dir(id), "chromium-launch.lock"), true)
	if err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			var reply map[string]bool
			return NewClient(id).Call(ctx, Request{Op: "browser.open", BrowserArgs: args}, &reply)
		}
		return err
	}
	defer lock.Close()
	var launch chromiumLaunch
	if err := NewClient(id).Call(ctx, Request{Op: "browser.prepare", BrowserArgs: args}, &launch); err != nil {
		return err
	}
	// A prior Chromium left an endpoint file after exit. It cannot identify
	// this launch and must never be used to close another process.
	if err := os.Remove(filepath.Join(computerBrowserProfile(id), "DevToolsActivePort")); err != nil && !os.IsNotExist(err) {
		return err
	}
	cmd := exec.Command(launch.Executable, launch.Args...)
	cmd.Env = mergeEnvironment(mergeEnvironment(os.Environ(), privateBusEnvironment(id)), map[string]string{"DYNA_COMPUTER_PROCESS": id})
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = pipes
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Every Chromium, whoever started it, runs under this launcher, so this is where a browser exit
	// ends its sign-in device, including a crash.
	defer func() { revokeBrowserDevice(id, time.Now()) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case sig := <-signals:
		_ = cmd.Process.Signal(sig)
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(4 * time.Second):
		_ = cmd.Process.Kill()
		return <-done
	}
}

func joinBrowserCgroup(id string) {
	b, err := os.ReadFile(filepath.Join(RuntimeDir(id), "cgroup"))
	if err == nil && strings.TrimSpace(string(b)) != "" {
		_ = os.WriteFile(filepath.Join(strings.TrimSpace(string(b)), "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0600)
	}
}

// Chromium shows a "Restore pages?" bubble when the last session did not exit
// cleanly. Mark the profile as cleanly closed before every launch so a killed
// session cannot trigger the prompt, and keep every other preference.
func markBrowserCleanExit(id string) error {
	path := filepath.Join(computerBrowserProfile(id), "Default", "Preferences")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	preferences := map[string]any{}
	if err := decoder.Decode(&preferences); err != nil {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	profile, _ := preferences["profile"].(map[string]any)
	if profile == nil {
		profile = map[string]any{}
		preferences["profile"] = profile
	}
	profile["exit_type"] = "Normal"
	profile["exited_cleanly"] = true
	return store.WriteJSON(path, preferences, 0600)
}

func prepareChromiumLaunch(ctx context.Context, id string, supplied []string) (chromiumLaunch, error) {
	v, err := settings.Load("")
	if err != nil {
		return chromiumLaunch{}, err
	}
	args, err := configuredBrowserFlags(id, v)
	if err != nil {
		return chromiumLaunch{}, err
	}
	if err := prepareBrowserProfile(ctx, id, v); err != nil {
		return chromiumLaunch{}, err
	}
	if err := markBrowserCleanExit(id); err != nil {
		return chromiumLaunch{}, err
	}
	executable := browserExecutable(v.BrowserPath)
	args = append(args, stripBrowserProfileArgs(supplied)...)
	// A computer's Chromium shares the owner's session bus, which copy-mine and
	// keyring-encrypted profiles need. Its notifications would then show on the
	// owner's desktop, so the browser has no Notification or Push API.
	args = append(args, "--user-data-dir="+computerBrowserProfile(id), "--profile-directory=Default", "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--hide-crash-restore-bubble", "--no-first-run", "--no-default-browser-check", "--disable-notifications")
	if browserconfig.BasicPasswordStore() {
		args = append(args, "--password-store=basic")
	}
	return chromiumLaunch{Executable: executable, Args: args}, nil
}

func configuredBrowserFlags(id string, v settings.Values) ([]string, error) {
	var args []string
	// The existing per-computer file remains one literal argument per line.
	b, err := os.ReadFile(chromiumFlagsFile(id))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			args = append(args, line)
		}
	}
	if v.BrowserUseFlagsFile {
		home, _ := os.UserHomeDir()
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		b, err := os.ReadFile(filepath.Join(config, "chromium-flags.conf"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		owner, err := splitBrowserFlags(string(b))
		if err != nil {
			return nil, fmt.Errorf("chromium-flags.conf: %w", err)
		}
		args = append(args, owner...)
	}
	extra, err := splitBrowserFlags(v.BrowserExtraFlags)
	if err != nil {
		return nil, fmt.Errorf("browser.extraFlags: %w", err)
	}
	args = append(args, extra...)
	for _, arg := range args {
		key, _, _ := strings.Cut(arg, "=")
		if key == "--user-data-dir" || key == "--profile-directory" || key == "--" || !strings.HasPrefix(arg, "--") {
			return nil, fmt.Errorf("browser flags must be flags and cannot change the private profile: %q", arg)
		}
	}
	return args, nil
}

// Tokenize shell-style quotes without evaluating substitutions or expansions.
func splitBrowserFlags(text string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	escaped, started, comment := false, false, false
	flush := func() {
		if started {
			out = append(out, word.String())
			word.Reset()
			started = false
		}
	}
	for _, ch := range text {
		if comment {
			if ch == '\n' {
				comment = false
			}
			continue
		}
		if escaped {
			word.WriteRune(ch)
			started = true
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			started = true
		case ' ', '\t', '\n', '\r':
			flush()
		case '#':
			if !started {
				comment = true
			} else {
				word.WriteRune(ch)
			}
		default:
			word.WriteRune(ch)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unclosed quote or escape")
	}
	flush()
	return out, nil
}

func stripBrowserProfileArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		key, _, hasValue := strings.Cut(args[i], "=")
		if key == "--user-data-dir" || key == "--profile-directory" {
			if !hasValue && i+1 < len(args) {
				i++
			}
			continue
		}
		if args[i] != "--" {
			out = append(out, args[i])
		}
	}
	return out
}

func profileBrowserPIDs(id string) map[int]string {
	out := map[int]string{}
	expected := "--user-data-dir=" + computerBrowserProfile(id)
	for pid, p := range processes() {
		if p.state == "Z" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(argv) == 1 {
			// Chromium rewrites its process title into one space-separated argument.
			if strings.Contains(argv[0], " "+expected+" ") || strings.HasSuffix(argv[0], " "+expected) {
				out[pid] = p.start
			}
			continue
		}
		for _, arg := range argv {
			if arg == expected {
				out[pid] = p.start
				break
			}
		}
	}
	return out
}

// Chromium must flush its SQLite and LevelDB files before save-back.
func stopProfileBrowsers(id string) {
	revokeBrowserDevice(id, time.Time{})
	pids := profileBrowserPIDs(id)
	if len(pids) == 0 {
		return
	}
	// Utility processes also carry --user-data-dir, but they do not own the
	// browser's shutdown. Wait for browser roots to flush and exit. The
	// supervisor kills and waits for remaining descendants before save-back.
	roots := make(map[int]string)
	for pid, start := range pids {
		argv, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err == nil && !strings.Contains(string(argv), "--type=") {
			roots[pid] = start
		}
	}
	// Browser.close flushes persistent cookies; SIGTERM alone can lose recent
	// writes because Chromium batches its cookie database updates.
	if closeProfileBrowser(id) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if !browserProcessesAlive(roots) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for pid, start := range pids {
		if p, ok := processInfo(pid); ok && p.start == start {
			_ = unix.Kill(pid, unix.SIGTERM)
		}
	}
	deadline := time.Now().Add(time.Second)
	for browserProcessesAlive(pids) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// Poll the selected processes rather than rescanning every process on the host.
// Start times prevent a reused PID from extending shutdown or receiving a signal.
func browserProcessesAlive(pids map[int]string) bool {
	for pid, start := range pids {
		if p, ok := processInfo(pid); ok && p.start == start && p.state != "Z" {
			return true
		}
	}
	return false
}

func closeProfileBrowser(id string) bool {
	// Wait for Chromium to accept Browser.close before closing the connection.
	// Dropping the connection after WriteJSON can discard the close command.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return browserCommand(ctx, id, "Browser.close", nil) == nil
}

// Repeat URL commands open a tab through the existing browser. They never
// start another Chromium or prepare a live profile.
func openBrowserURLs(ctx context.Context, id string, args []string) error {
	if len(profileBrowserPIDs(id)) == 0 {
		return fmt.Errorf("Chromium is starting; retry the URL command")
	}
	if len(args) == 0 {
		args = []string{"about:blank"}
	}
	if err := validateBrowserURLs(args); err != nil {
		return err
	}
	for _, arg := range args {
		if err := browserCommand(ctx, id, "Target.createTarget", map[string]any{"url": arg}); err != nil {
			return err
		}
	}
	return nil
}

func browserCommand(ctx context.Context, id, method string, params map[string]any) error {
	endpoint, err := browserEndpoint(id)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(bounded, endpoint, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := conn.WriteJSON(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var response struct {
			ID    int
			Error *struct{ Message string }
		}
		if err := json.Unmarshal(message, &response); err != nil {
			return err
		}
		if response.ID == 1 {
			if response.Error != nil {
				return fmt.Errorf("Chromium: %s", response.Error.Message)
			}
			return nil
		}
	}
}

func browserEndpoint(id string) (string, error) {
	b, err := os.ReadFile(filepath.Join(computerBrowserProfile(id), "DevToolsActivePort"))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("invalid Chromium endpoint")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(lines[1], "/devtools/browser/") || strings.ContainsAny(lines[1], "?# \t\r") {
		return "", fmt.Errorf("invalid Chromium endpoint")
	}
	return "ws://127.0.0.1:" + strconv.Itoa(port) + lines[1], nil
}

func validateBrowserURLs(args []string) error {
	for _, arg := range args {
		parsed, err := url.Parse(arg)
		if err != nil || parsed.Scheme == "" || strings.HasPrefix(arg, "-") {
			return fmt.Errorf("this computer already runs Chromium; only URLs can be opened again")
		}
	}
	return nil
}
