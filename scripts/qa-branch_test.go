package main

import (
	"bytes"
	"dyna-agent/internal/engine"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// Exercise the actual embedded controller, including navigation and unknown endpoints.
// The fake CDP module avoids a browser startup and real engine calls.
func TestQABrowserControllerFailsClosed(t *testing.T) {
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
	cmd := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", browserQAScript)
	cmd.Stdin = bytes.NewReader(config)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("QA controller: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("QA policy checks passed: 29")) {
		t.Fatalf("missing policy checks: %s", out)
	}
	// The tab cannot hold the computer stream, and QA must not report that as
	// an app failure: the socket relays desktop input.
	if !bytes.Contains(out, []byte("The computer stream stays closed in this tab")) {
		t.Fatalf("missing the computer stream note: %s", out)
	}
	if !bytes.Contains(out, []byte("QA teardown ignored the pending cookie error")) {
		t.Fatalf("pending route teardown was not exercised: %s", out)
	}
	if bytes.Contains(out, []byte("not-logged")) {
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
