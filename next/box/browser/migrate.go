package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	// JarFilename is the cookie jar file inside <data>/browsers/<name>/.
	JarFilename = "jar.json"
	// devToolsReadLimit accommodates jars up to and well beyond MaxJarBytes.
	devToolsReadLimit = 64 << 20
)

// HasV1Profile reports whether dir holds a v1 Chromium profile with a Cookies database.
func HasV1Profile(dir string) bool {
	for _, rel := range []string{
		filepath.Join("Default", "Cookies"),
		filepath.Join("Default", "Network", "Cookies"),
	} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// MigrateV1Profile checks whether dir holds a v1 Chromium profile (Default/Cookies) without
// a jar.json yet. When found, it extracts cookies via headless Chromium, writes jar.json
// atomically (0600), and removes the v1 profile files and prevDir.
// If extract is nil, ExtractV1Cookies is used.
func MigrateV1Profile(ctx context.Context, dir, prevDir string, now time.Time, extract func(context.Context, string) ([]Cookie, error)) (*Jar, bool, error) {
	jarPath := filepath.Join(dir, JarFilename)
	if _, err := os.Stat(jarPath); err == nil {
		if prevDir != "" {
			_ = os.RemoveAll(prevDir)
		}
		return nil, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if !HasV1Profile(dir) {
		if prevDir != "" {
			_ = os.RemoveAll(prevDir)
		}
		return nil, false, nil
	}
	if extract == nil {
		extract = ExtractV1Cookies
	}
	cookies, err := extract(ctx, dir)
	if err != nil {
		return nil, false, err
	}
	jar, encoded, err := NewJarFromCookies(cookies, now)
	if err != nil {
		return nil, false, err
	}
	if err := SaveJarBytes(jarPath, encoded); err != nil {
		return nil, false, err
	}
	if err := CleanupV1Profile(dir, prevDir); err != nil {
		return jar, true, err
	}
	return jar, true, nil
}

// CleanupV1Profile removes all v1 profile entries in dir except jar.json, and removes prevDir.
func CleanupV1Profile(dir, prevDir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == JarFilename {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if prevDir != "" {
		if err := os.RemoveAll(prevDir); err != nil {
			return err
		}
	}
	return nil
}

func findChromium() (string, error) {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("chromium binary not found on host; v1 profile preserved")
}

// ExtractV1Cookies starts a temporary headless Chromium against profileDir and reads all
// cookies through DevTools Storage.getCookies. Errors never include cookie values.
func ExtractV1Cookies(ctx context.Context, profileDir string) ([]Cookie, error) {
	bin, err := findChromium()
	if err != nil {
		return nil, err
	}
	for _, stale := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket", "DevToolsActivePort"} {
		if err := os.Remove(filepath.Join(profileDir, stale)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("clear stale %s: %w", stale, err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.Command(bin,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		"--password-store=basic",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--user-data-dir="+profileDir,
		"about:blank",
	)
	cmd.Env = withoutEnv(os.Environ(), "DISPLAY")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start headless chromium: %w", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		select {
		case <-done:
			return
		default:
		}
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
	}()

	wsURL, err := waitDevToolsActivePort(runCtx, filepath.Join(profileDir, "DevToolsActivePort"), done, &waitErr)
	if err != nil {
		return nil, err
	}

	conn, _, err := websocket.Dial(runCtx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to headless chromium DevTools: %w", err)
	}
	conn.SetReadLimit(devToolsReadLimit)
	defer conn.CloseNow()

	rawResult, err := cdpCall(runCtx, conn, 1, "Storage.getCookies", json.RawMessage(`{}`))
	if err != nil {
		return nil, fmt.Errorf("Storage.getCookies: %w", err)
	}
	var payload struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := json.Unmarshal(rawResult, &payload); err != nil {
		return nil, errors.New("decode Storage.getCookies response")
	}
	cookies := make([]Cookie, 0, len(payload.Cookies))
	for i, raw := range payload.Cookies {
		c, err := ParseCookie(raw)
		if err != nil {
			return nil, fmt.Errorf("migrated cookie %d: %w", i, err)
		}
		cookies = append(cookies, c)
	}

	// Ask Chromium to close cleanly so it stops touching profileDir before cleanup.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = cdpCall(closeCtx, conn, 2, "Browser.close", nil)
	closeCancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")

	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return cookies, nil
}

func waitDevToolsActivePort(ctx context.Context, path string, done <-chan struct{}, waitErr *error) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
				wsPath := strings.TrimSpace(lines[1])
				if err == nil && port > 0 && port <= 65535 && strings.HasPrefix(wsPath, "/") {
					return fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath), nil
				}
			}
		}
		select {
		case <-done:
			return "", fmt.Errorf("headless chromium exited before opening DevTools: %v", *waitErr)
		case <-ctx.Done():
			return "", errors.New("timed out waiting for headless chromium DevTools")
		case <-ticker.C:
		}
	}
}

func cdpCall(ctx context.Context, conn *websocket.Conn, id int, method string, params json.RawMessage) (json.RawMessage, error) {
	req, err := json.Marshal(struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
		return nil, err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, errors.New("invalid DevTools response")
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, errors.New(resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}
