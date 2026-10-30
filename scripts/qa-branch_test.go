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

func TestParseQASteps(t *testing.T) {
	steps, err := parseQASteps(strings.NewReader(`[
		{"viewport":{"width":1280,"height":800}},
		{"goto":"/rooms"},
		{"click":"button.new-room"},
		{"type":{"selector":"input[name=name]","text":"QA"}},
		{"waitFor":"text=QA"},
		{"screenshot":"/tmp/qa.png"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 6 || steps[0].Viewport == nil || steps[0].Viewport.Width != 1280 || steps[0].Viewport.Height != 800 ||
		steps[1].Goto == nil || *steps[1].Goto != "/rooms" ||
		steps[2].Click == nil || *steps[2].Click != "button.new-room" ||
		steps[3].Type == nil || steps[3].Type.Selector != "input[name=name]" || steps[3].Type.Text != "QA" ||
		steps[4].WaitFor == nil || *steps[4].WaitFor != "text=QA" ||
		steps[5].Screenshot == nil || *steps[5].Screenshot != "/tmp/qa.png" {
		t.Fatalf("unexpected steps: %#v", steps)
	}
	for name, input := range map[string]string{
		"empty list":       `[]`,
		"no action":        `[{}]`,
		"multiple actions": `[{"goto":"/","screenshot":"qa.png"}]`,
		"bad viewport":     `[{"viewport":{"width":0,"height":800}}]`,
		"empty selector":   `[{"click":""}]`,
		"unknown field":    `[{"delay":1}]`,
		"trailing value":   `[{"goto":"/"}] []`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseQASteps(strings.NewReader(input)); err == nil {
				t.Fatalf("accepted %s", input)
			}
		})
	}
}

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
	for name, body := range map[string]string{"index.html": "<head></head><body>branch-shell</body>", "asset.js": "branch-asset", "asset.mjs": "branch-module"} {
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
		"Endpoint": "ws://127.0.0.1:123/devtools/browser/test", "Module": module, "Web": dir,
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
	var printed []string
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			printed = append(printed, scanner.Text())
		}
	}()
	// The controller ends the run after one report, so this reads to EOF: a
	// report that never comes fails here with what the controller printed,
	// instead of waiting for the suite timeout.
	<-finished
	if err := cmd.Wait(); err != nil {
		t.Fatalf("QA controller: %v\n%s\n%s", err, strings.Join(printed, "\n"), stderr.String())
	}
	out := strings.Join(printed, "\n")
	if !strings.Contains(out, "QA policy checks passed: 30") {
		t.Fatalf("missing policy checks: %s", out)
	}
	for _, want := range []string{
		"QA page CDP target: qa-target",
		"QA page CDP websocket: ws://127.0.0.1:123/devtools/page/qa-target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	// A control that needs a write, a socket or a newer server never appears in
	// this tab, and the banner must say so before an agent blames the branch.
	// The room's live event stream is an EventSource over an allowed read, so
	// the banner names socket streams only.
	if !strings.Contains(out, "Read-only QA cannot show settings writes, live streams that need a WebSocket (the computer viewer and voice calls), or server fields newer than the installed engine.") {
		t.Fatalf("missing the read-only limits: %s", out)
	}
	// A timed-out step needs one documented call, not a guess.
	if !strings.Contains(out, "call globalThis.dynaQAReport() in the QA page") {
		t.Fatalf("missing the report call: %s", out)
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
	screenshot := ""
	for _, line := range printed {
		if value, ok := strings.CutPrefix(line, "QA blocked kinds: "); ok {
			kinds = value
		}
		if value, ok := strings.CutPrefix(line, "QA page screenshot: "); ok {
			screenshot = value
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
	// A blank tab needs the page errors and failed assets, named by kind. The
	// missing file reports the reason the route gave, not a bare 404.
	failures := ""
	for _, line := range printed {
		if value, ok := strings.CutPrefix(line, "QA page errors and failed assets: "); ok {
			failures = value
		}
	}
	if want := "asset unavailable, page error, console error, script request failed, HTTP 404 asset request"; failures != want {
		t.Errorf("failed assets = %q; want %q", failures, want)
	}
	if !strings.HasPrefix(screenshot, scratch+string(filepath.Separator)) || !strings.HasSuffix(screenshot, string(filepath.Separator)+"page.png") {
		t.Fatalf("screenshot = %q; want a page.png inside the QA scratch space %q", screenshot, scratch)
	}
	if body, err := os.ReadFile(screenshot); err != nil || string(body) != "PNG" {
		t.Errorf("saved screenshot = %q, %v; want the tab's picture", body, err)
	}
	if strings.Contains(out, "not-logged") {
		t.Fatal("controller logged a credential")
	}
}

func TestQABrowserControllerRunsStepsAndReportsFailures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	module, err := filepath.Abs("testdata/browser_qa_steps_fake.mjs")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("actions", func(t *testing.T) {
		screenshot := filepath.Join(t.TempDir(), "steps.png")
		encodedPath, err := json.Marshal(screenshot)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runQAStepsController(t, node, module, `[
			{"viewport":{"width":1280,"height":800}},
			{"goto":"/rooms"},
			{"click":"button.save"},
			{"type":{"selector":"input[name=name]","text":"QA"}},
			{"waitFor":"text=Ready"},
			{"screenshot":`+string(encodedPath)+`}
		]`)
		if err != nil {
			t.Fatalf("QA steps: %v\n%s", err, out)
		}
		for _, want := range []string{
			"QA fake viewport: 1280x800",
			"QA fake goto: http://127.0.0.1:7457/rooms",
			"QA fake click: button.save",
			"QA fake type: input[name=name] <= QA",
			"QA fake waitFor: text=Ready",
			"QA fake screenshot: " + screenshot,
			"QA steps complete.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		if body, err := os.ReadFile(screenshot); err != nil || string(body) != "PNG" {
			t.Errorf("step screenshot = %q, %v", body, err)
		}
	})

	t.Run("failure diagnostics", func(t *testing.T) {
		out, err := runQAStepsController(t, node, module, `[
			{"waitFor":"body"},
			{"goto":"https://evil.test/"}
		]`)
		if err == nil {
			t.Fatalf("cross-origin goto succeeded:\n%s", out)
		}
		for _, want := range []string{
			"QA step 2 failed: goto must stay on http://127.0.0.1:7457",
			"QA blocked nothing yet.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		reportPath := ""
		for _, line := range strings.Split(out, "\n") {
			if value, ok := strings.CutPrefix(line, "QA page screenshot: "); ok {
				reportPath = value
			}
		}
		if reportPath == "" {
			t.Fatalf("failure saved no diagnostic screenshot:\n%s", out)
		}
		if body, err := os.ReadFile(reportPath); err != nil || string(body) != "PNG" {
			t.Errorf("diagnostic screenshot = %q, %v", body, err)
		}
	})
}

func runQAStepsController(t *testing.T, node, module, rawSteps string) (string, error) {
	t.Helper()
	steps, err := parseQASteps(strings.NewReader(rawSteps))
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(struct {
		Endpoint    string
		Module      string
		Web         string
		ReadMethods []string
		Steps       []qaStep
	}{"ws://127.0.0.1:123/devtools/browser/test", module, t.TempDir(), engine.ReadOnlyWebMethods(), steps})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", browserQAScript)
	cmd.Env = append(os.Environ(), "DYNA_SCRATCH="+t.TempDir())
	cmd.Stdin = bytes.NewReader(config)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
