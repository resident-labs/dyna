package watch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/cibudget"
)

const spanGoFile = `package x

import "testing"

func TestChangedSlow(t *testing.T) {
	x := 1
	y := 2
	_ = x + y
}

func TestFast(t *testing.T) {
	_ = 1
}
`

const spanJSFile = `import test from 'node:test'

test('the sidebar renders', () => {
  const a = 1
  const b = "not ) a close"
  return a
})

test('another case', () => {})
`

func TestChangedByPRMatchesTestSpans(t *testing.T) {
	goTest := cibudget.Test{Package: "dyna-agent/internal/x", Name: "TestChangedSlow"}
	jsTest := cibudget.Test{Package: "test", Name: "the sidebar renders"}
	for name, tc := range map[string]struct {
		test cibudget.Test
		file prFile
		want bool
	}{
		// The hunk header names another function and no added line names the test.
		"go body edit":     {goTest, prFile{Name: "internal/x/x_test.go", Content: spanGoFile, Patch: "@@ -6,3 +6,3 @@ func helper() {\n \tx := 1\n-\ty := 3\n+\ty := 2\n \t_ = x + y\n"}, true},
		"go deleted line":  {goTest, prFile{Name: "internal/x/x_test.go", Content: spanGoFile, Patch: "@@ -6,4 +6,3 @@\n \tx := 1\n-\tz := 0\n \ty := 2\n \t_ = x + y\n"}, true},
		"go other test":    {goTest, prFile{Name: "internal/x/x_test.go", Content: spanGoFile, Patch: "@@ -12,1 +12,1 @@ func TestFast(t *testing.T) {\n-\t_ = 0\n+\t_ = 1\n"}, false},
		"go other package": {goTest, prFile{Name: "internal/z/x_test.go", Content: spanGoFile, Patch: "@@ -6,1 +6,1 @@\n-\tx := 0\n+\tx := 1\n"}, false},
		"js body edit":     {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: spanJSFile, Patch: "@@ -4,1 +4,1 @@\n-  const a = 0\n+  const a = 1\n"}, true},
		"js other test":    {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: spanJSFile, Patch: "@@ -9,1 +9,1 @@\n-test('another case', () => { })\n+test('another case', () => {})\n"}, false},
		"missing patch":    {goTest, prFile{Name: "internal/x/x_test.go", Content: spanGoFile}, true},
		// An unread file falls back to names on changed lines; it never counts every test.
		"missing content":      {jsTest, prFile{Name: "web/src/Sidebar.test.js", Patch: "@@ -9,1 +9,1 @@\n-x\n+y\n"}, false},
		"missing content name": {jsTest, prFile{Name: "web/src/Sidebar.test.js", Patch: "@@ -1 +1,2 @@\n+test('the sidebar renders', () => {\n"}, true},
		"missing content go":   {goTest, prFile{Name: "internal/x/x_test.go", Patch: "@@ -6,1 +6,1 @@ func TestChangedSlow(t *testing.T) {\n-\tx := 0\n+\tx := 1\n"}, true},
		// Only complete names count: a longer name that starts with the test's is another test.
		"missing content go prefix": {goTest, prFile{Name: "internal/x/x_test.go", Patch: "@@ -20,0 +20,2 @@\n+func TestChangedSlowOther(t *testing.T) {\n+}\n"}, false},
		"missing content js prefix": {jsTest, prFile{Name: "web/src/Sidebar.test.js", Patch: "@@ -1 +1,2 @@\n+test('the sidebar renders details', () => {\n"}, false},
		"parent package":            {cibudget.Test{Package: "dyna-agent/internal/harness/runtime", Name: "TestSwitch"}, prFile{Name: "internal/harness/x_test.go", Content: "package harness\n\nfunc TestSwitch(t *testing.T) {\n}\n", Patch: "@@ -3,1 +3,1 @@\n-x\n+func TestSwitch(t *testing.T) {\n"}, false},
		"js spaced only":            {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: "test.only ('the sidebar renders', () => {\n  const a = 1\n})\n", Patch: "@@ -2,1 +2,1 @@\n-  const a = 0\n+  const a = 1\n"}, true},
		"js multiline":              {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: "test(\n  'the sidebar renders',\n  () => {\n    const a = 1\n  })\n", Patch: "@@ -4,1 +4,1 @@\n-    const a = 0\n+    const a = 1\n"}, true},
		"js block comment":          {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: "test('the sidebar renders', () => {\n  /* )) */\n  const a = 1\n})\n", Patch: "@@ -3,1 +3,1 @@\n-  const a = 0\n+  const a = 1\n"}, true},
		"js regex literal":          {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: "test('the sidebar renders', () => {\n  const r = /\\)}/\n  const a = 1\n})\n", Patch: "@@ -3,1 +3,1 @@\n-  const a = 0\n+  const a = 1\n"}, true},
		"js name in constant":       {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: "const label = 'the sidebar renders'\ntest('fast', () => {})\ntest('the sidebar renders', () => {\n  work()\n})\n", Patch: "@@ -4,1 +4,1 @@\n-  work(1)\n+  work()\n"}, true},
		"js not in file":            {jsTest, prFile{Name: "web/src/Other.test.js", Content: "test('other', () => {})\n", Patch: "@@ -1,1 +1,1 @@\n-x\n+test('other', () => {})\n"}, false},
		"js test by file":           {cibudget.Test{Package: "test", Name: "src/Sidebar.test.js"}, prFile{Name: "web/src/Sidebar.test.js", Content: spanJSFile, Patch: "@@ -9,1 +9,1 @@\n-x\n+y\n"}, true},
		// A large diff has no patch. The file counts for a test it can hold.
		"large diff js name":   {jsTest, prFile{Name: "web/src/Sidebar.test.js", Content: spanJSFile}, true},
		"large diff js absent": {jsTest, prFile{Name: "web/src/Other.test.js", Content: "test('other', () => {})\n"}, false},
		"large diff js path":   {cibudget.Test{Package: "test", Name: "test/src/lib/files/FileBrowser.test.js"}, prFile{Name: "web/src/lib/settings/profileUse.test.js", Content: "test('profile saves', () => {})\n"}, false},
		"large diff unread":    {jsTest, prFile{Name: "web/src/Sidebar.test.js"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := changedByPR(tc.test, []prFile{tc.file}); got != tc.want {
				t.Fatalf("changedByPR = %v, want %v", got, tc.want)
			}
		})
	}
}

func contentFixture(e *testEnv, name, content string) {
	body, _ := json.Marshal(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
	e.f.Raw[fmt.Sprintf("repos/owner/repo/contents/%s?ref=%s", name, e.f.Pull.Head.SHA)] = "HTTP/2.0 200 OK\r\n\r\n" + string(body)
}

// The watcher reads each changed test file at the PR head to find test spans.
func TestTestBudgetFailsBodyOnlyEditToSlowTest(t *testing.T) {
	e := scopeEnv(t, prFile{Name: "internal/x/x_test.go", Patch: "@@ -6,3 +6,3 @@ func helper() {\n \tx := 1\n-\ty := 3\n+\ty := 2\n \t_ = x + y\n"})
	contentFixture(e, "internal/x/x_test.go", spanGoFile)
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" || r.CIReason[:len("tests over 1s: dyna-agent/internal/x/TestChangedSlow")] != "tests over 1s: dyna-agent/internal/x/TestChangedSlow" {
		t.Fatalf("ci=%s reason=%q", r.CI, r.CIReason)
	}
}

// A failed file-list read retries, then warns. It never fails the PR.
func TestTestBudgetUnreadPRFilesRetryThenWarn(t *testing.T) {
	e := scopeEnv(t)
	e.f.Raw["repos/owner/repo/pulls/17/files?per_page=100"] = "HTTP/2.0 502 Bad Gateway\r\n\r\n{\"message\":\"bad gateway\"}"
	e.track()
	e.tick()
	if r := e.record(); r.CI == "failed" || r.CITestRuns["81/1"] != "" || r.CITestRetries["81/1"] != 1 {
		t.Fatalf("first failed read decided the budget: ci=%s runs=%v retries=%v", r.CI, r.CITestRuns, r.CITestRetries)
	}
	for range 3 {
		e.f.Pull.Updated = e.clock.Format(time.RFC3339)
		e.tick()
	}
	if r := e.record(); r.CI == "failed" || !strings.HasPrefix(r.CITestRuns["81/1"], "warning: ") {
		t.Fatalf("unread PR files failed the PR: ci=%s runs=%v retries=%v", r.CI, r.CITestRuns, r.CITestRetries)
	}
}

// A failed content read must not make every slow web test the PR's own.
func TestTestBudgetUnreadContentOnlyWarns(t *testing.T) {
	e := scopeEnv(t, prFile{Name: "web/src/profileUse.test.js", Patch: "@@ -3,1 +3,1 @@\n-  a()\n+  b()\n"})
	e.f.Raw[fmt.Sprintf("repos/owner/repo/contents/web/src/profileUse.test.js?ref=%s", e.f.Pull.Head.SHA)] = "HTTP/2.0 502 Bad Gateway\r\n\r\n{\"message\":\"bad gateway\"}"
	e.track()
	e.tick()
	if r := e.record(); r.CI == "failed" || !strings.HasPrefix(r.CITestRuns["81/1"], "warning: ") {
		t.Fatalf("an unread file counted other web tests: ci=%s runs=%v", r.CI, r.CITestRuns)
	}
}
