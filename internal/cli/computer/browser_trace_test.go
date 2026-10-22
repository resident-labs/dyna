package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/nodedeps"
)

// fakeChromium is a minimal playwright-core and CDP session: one page with 3 requests, 3 long tasks over
// two origins and 1 console error. The injected document script reports through the CDP binding.
const fakeChromium = `
const handlers = {};
const observers = [], listeners = {}, cdpListeners = {};
let documentScript;
globalThis.PerformanceObserver = class { constructor(cb) { this.cb = cb; } observe({ type }) { observers.push({ type, cb: this.cb }); } };
globalThis.addEventListener = (name, f) => { listeners[name] = f; };
// load runs the injected script as one document, then fires what it registered.
// withBinding false models a document where the binding is not there.
function load(isTop, origin, longtask, mark, dcl, withBinding = true) {
  observers.length = 0;
  for (const k in listeners) delete listeners[k];
  Object.defineProperty(globalThis, "performance", { configurable: true, value: { timeOrigin: origin, now: () => dcl } });
  globalThis.top = isTop ? globalThis : {};
  if (withBinding) globalThis.__dynaTrace = (payload) => cdpListeners["Runtime.bindingCalled"]?.({ name: "__dynaTrace", payload });
  else delete globalThis.__dynaTrace;
  (0, eval)(documentScript);
  for (const o of observers) o.cb({ getEntries: () => [{ duration: longtask, startTime: mark }] });
  listeners.DOMContentLoaded?.();
}
import { appendFileSync } from "node:fs";
const cdp = {
  on: (event, cb) => { cdpListeners[event] = cb; },
  send: async (m, params) => {
    appendFileSync(process.argv[4] + "/cdp.log", JSON.stringify([m, params]) + "\n");
    if (m === "Browser.getVersion") return { userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36" };
    if (m === "Page.addScriptToEvaluateOnNewDocument") { documentScript = params.source; return { identifier: "script-1" }; }
    return m === "Performance.getMetrics" ? { metrics: [{ name: "JSHeapUsedSize", value: heap.shift() }] } : {};
  },
};
const heap = [1000, 5000];
const page = {
  on: (e, f) => { handlers[e] = f; },
  context: () => ({ newCDPSession: async () => cdp }),
  goto: async () => {
    for (let i = 0; i < 3; i++) handlers.request();
    handlers.console({ type: () => "error", text: () => "boom" });
    // A child frame reports first, with a timing that must not count for the page.
    load(false, 7, 999, 1, 7);
    // The main frame: a mark at 123.4 ms, DOMContentLoaded at 500 ms, a long task of 250 ms.
    load(true, 1, 250, 123.4, 500);
    // A navigation to another origin: its long task counts, its timing does not replace the first load.
    load(true, 2, 900, 5, 10);
    // A report from a script an older trace left on the page must not join this result.
    globalThis.__dynaTrace(JSON.stringify(["older-trace", "longtask", 5000, 1]));
    // A document whose binding is missing must report nothing and must not throw.
    load(true, 1, 999, 1, 1, false);
    // A third document, back on the first origin, adds its own long task.
    load(true, 1, 300, 700, 900);
  },
  reload: async () => {},
  waitForLoadState: async () => {},
  evaluate: async (fn) => fn(),
  screenshot: async ({ path }) => (await import("node:fs/promises")).writeFile(path, "png"),
  url: () => "http://example.test/",
};
export const chromium = { connectOverCDP: async () => ({ on: () => {}, contexts: () => [{ pages: () => [page] }] }) };
`

// nodeBrowserFixture runs the real holder scripts against fakeChromium.
func nodeBrowserFixture(t *testing.T, endpoint string) func(...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	call := browserFixture(t, func(_ context.Context, r computers.Request) (any, error) {
		if r.Op == "register" {
			return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
		}
		return computers.Browser{Endpoint: endpoint}, nil
	})
	installPlaywright = nodedeps.Executor(func(_ context.Context, dir string, _ []string, _ io.Writer) error {
		module := filepath.Join(dir, "node_modules", "playwright-core")
		if err := os.MkdirAll(module, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(module, "index.mjs"), []byte(fakeChromium), 0o644)
	})
	return call
}

func TestBrowserTraceReportsR13Numbers(t *testing.T) {
	call := nodeBrowserFixture(t, "ws://127.0.0.1:1/devtools/browser/id")
	if _, err := call("trace", "stop"); err == nil || !strings.Contains(err.Error(), "no trace is running") {
		t.Fatalf("stop without start: %v", err)
	}
	if out, err := call("trace", "start", "http://example.test/"); err != nil || !strings.Contains(out, "trace started") {
		t.Fatalf("start: %q %v", out, err)
	}
	if _, err := call("trace", "start"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start: %v", err)
	}
	out, err := call("trace", "stop")
	if err != nil {
		t.Fatal(err)
	}
	var report TraceReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("report %q: %v", out, err)
	}
	if report.TimeToUsableMS == nil || *report.TimeToUsableMS != 123 || report.TimeToUsableSource != "mark" ||
		report.LongTasks != 3 || report.HeapGrowthBytes != 4000 || report.RequestCount != 3 ||
		len(report.ConsoleErrors) != 1 || report.ConsoleErrors[0] != "boom" || report.Note == "" {
		t.Fatalf("report: %+v", report)
	}
	if _, err := os.Stat(report.Screenshot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	// The trace leaves nothing behind for the next one on the same tab.
	log, err := os.ReadFile(filepath.Join(filepath.Dir(report.Screenshot), "cdp.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Page.removeScriptToEvaluateOnNewDocument",{"identifier":"script-1"}`, `"Runtime.removeBinding",{"name":"__dynaTrace"}`} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("trace left its document script behind: missing %s in\n%s", want, log)
		}
	}
}

func TestBrowserHolderIgnoresAStaleOrReusedPID(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	start, ok := computers.ProcessStart(os.Getpid())
	if !ok {
		t.Fatal("no start time for this process")
	}
	for name, content := range map[string]string{
		"reused pid":   fmt.Sprintf("%d %s", os.Getpid(), start+"1"),
		"old format":   fmt.Sprint(os.Getpid()),
		"gone process": "999999999 1",
		"garbage":      "x",
	} {
		os.WriteFile(pidFile, []byte(content), 0o600)
		if pid := holderPID(dir); pid != 0 {
			t.Fatalf("%s: holderPID = %d", name, pid)
		}
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Fatalf("%s: stale pid file kept", name)
		}
	}
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d %s", os.Getpid(), start)), 0o600)
	if pid := holderPID(dir); pid != os.Getpid() {
		t.Fatalf("live holder: pid = %d", pid)
	}
}
