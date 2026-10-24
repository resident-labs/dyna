package main

import (
	"bufio"
	"bytes"
	"dyna-agent/internal/engine"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQAWebDirRequiresBuiltIndex(t *testing.T) {
	if _, err := qaWebDir(""); err == nil {
		t.Fatal("accepted missing directory")
	}
	dir := t.TempDir()
	if _, err := qaWebDir(dir); err == nil {
		t.Fatal("accepted missing index")
	}
	index := filepath.Join(dir, "index.html")
	if err := os.WriteFile(index, []byte("<html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := qaWebDir(dir); err == nil {
		t.Fatal("accepted index without head")
	}
	if err := os.WriteFile(index, []byte("<html><head></head></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := qaWebDir(dir)
	if err != nil || got != dir {
		t.Fatalf("built directory: %q, %v", got, err)
	}
}

// Exercise the actual embedded controller: navigation, unknown endpoints, and
// the report an agent asks for when a step times out. The fake CDP module
// avoids a browser startup and real engine calls.
func TestQABrowserControllerBlocksWritesAndExplainsTimeouts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"index.html": "<head></head><body>branch-shell</body>", "asset.js": "branch-asset"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("must not leave asset root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("testdata/browser_qa_fake.mjs")
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"Endpoint": "test-endpoint", "Module": module, "Web": dir,
		"ReadMethods": engine.ReadOnlyWebMethods(),
	})
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	cmd := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", browserQAScript)
	cmd.Env = append(os.Environ(), "DYNA_SCRATCH="+scratch)
	cmd.Stdin = bytes.NewReader(config)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// A failure before the controller ends must not leave its process behind.
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	lines := make(chan string, 256)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var printed []string
	find := func(marker string) string {
		t.Helper()
		for line := range lines {
			printed = append(printed, line)
			if strings.Contains(line, marker) {
				return line
			}
		}
		t.Fatalf("QA printed no line with %q: %v\n%s", marker, printed, stderr.String())
		return ""
	}
	// The announcement names the one file that asks for a report, so an agent
	// whose step timed out has a documented way to learn why it did.
	announced := find("When a step times out, create ")
	request, _, _ := strings.Cut(strings.TrimPrefix(announced, "When a step times out, create "), " (mkdir -p")
	if request == "" {
		t.Fatalf("announcement names no request file: %q", announced)
	}
	if err := os.WriteFile(request, nil, 0600); err != nil {
		t.Fatal(err)
	}
	screenshot, _, _ := strings.Cut(strings.TrimPrefix(find("QA page screenshot: "), "QA page screenshot: "), " ")
	if want := filepath.Join(filepath.Dir(request), "page.png"); screenshot != want {
		t.Errorf("screenshot = %q; want %q beside the request", screenshot, want)
	}
	if body, err := os.ReadFile(screenshot); err != nil || string(body) != "PNG" {
		t.Errorf("saved screenshot = %q, %v; want the tab's picture", body, err)
	}
	// An agent ends QA the way it started it: it stops the command.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	for line := range lines {
		printed = append(printed, line)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("QA controller: %v\n%s\n%s", err, strings.Join(printed, "\n"), stderr.String())
	}
	out := strings.Join(printed, "\n")
	if !strings.Contains(out, "QA policy checks passed: 29") {
		t.Fatalf("missing policy checks: %s", out)
	}
	// A control that needs a write, a socket or a newer server never appears in
	// this tab, and the banner must say so before an agent blames the branch.
	// The room's live event stream is an EventSource over an allowed read, so
	// the banner names socket streams only.
	if !strings.Contains(out, "Read-only QA cannot show settings writes, live streams that need a WebSocket (the computer viewer and voice calls), or server fields newer than the installed engine.") {
		t.Fatalf("missing the read-only limits: %s", out)
	}
	// The tab cannot hold the computer stream, and QA must not report that as
	// an app failure: the socket relays desktop input.
	if !strings.Contains(out, "The computer stream stays closed in this tab") {
		t.Fatalf("missing the computer stream note: %s", out)
	}
	if !strings.Contains(out, "QA teardown ignored the pending cookie error") {
		t.Fatalf("pending route teardown was not exercised: %s", out)
	}
	// The report names kinds with their counts, and never a URL, a header or a
	// cookie: the fake drives the blocked requests the policy checks cover.
	kinds := ""
	for _, line := range printed {
		if strings.HasPrefix(line, "QA blocked kinds: ") {
			kinds = strings.TrimPrefix(line, "QA blocked kinds: ")
		}
	}
	for _, want := range []string{"POST API request x", "socket", "GET external request x"} {
		if !strings.Contains(kinds, want) {
			t.Errorf("blocked kinds = %q; want %q", kinds, want)
		}
	}
	for _, leak := range []string{"http", "/api", "not-logged", "cookie"} {
		if strings.Contains(kinds, leak) {
			t.Errorf("blocked kinds = %q; it leaks %q", kinds, leak)
		}
	}
	if strings.Contains(out, "not-logged") {
		t.Fatal("controller logged a credential")
	}
}

func TestBrowserConnectionUsesManagedModule(t *testing.T) {
	endpoint, module, err := browserConnection("ws://127.0.0.1:123/devtools/browser/test\nconst { chromium } = await import(\"/tmp/managed-playwright/index.mjs\");\n")
	if err != nil || endpoint != "ws://127.0.0.1:123/devtools/browser/test" || module != "/tmp/managed-playwright/index.mjs" {
		t.Fatalf("browser connection: %q, %q, %v", endpoint, module, err)
	}
	for _, output := range []string{"", "ws://evil.test/\nconst { chromium } = await import(\"/tmp/a\");", "ws://127.0.0.1:1/\nwrong", "ws://127.0.0.1:1/\nimport(\"relative\");"} {
		if _, _, err := browserConnection(output); err == nil {
			t.Fatalf("accepted malformed browser output: %q", output)
		}
	}
}
