package view

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store"
)

func savedLocalCI(t *testing.T, result localci.Result) watch.Record {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repo: %v: %s", err, out)
	}
	result.Repo = filepath.Join(repo, ".git")
	result.SHA = strings.Repeat("a", 40)
	hash := sha256.Sum256([]byte(result.Repo))
	dir := filepath.Join(store.DataDir(), "local-ci", hex.EncodeToString(hash[:16]), result.SHA)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "result.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return watch.Record{Kind: "local", RepoPath: repo, Head: result.SHA}
}

func TestLocalCIChecksUseSavedSuitesAndSerializeDuration(t *testing.T) {
	record := savedLocalCI(t, localci.Result{Status: "failed", Suites: []localci.SuiteResult{
		{Suite: "go", Status: "failed", Duration: 1250 * time.Millisecond, Failures: []string{"pkg/TestBroken"}, LogTail: "assertion failed", SlowTests: []localci.TestTiming{{Name: "pkg/TestSlow", Duration: 12 * time.Second}}},
		{Suite: "relay", Status: "passed", Duration: 700 * time.Millisecond},
		{Suite: "frontend", Status: "error", Error: "transport failed"},
	}})
	got := localCIChecks(watch.ReviewFacts{}, record)
	if len(got.Checks) != 3 {
		t.Fatalf("checks=%+v", got.Checks)
	}
	if got.Checks[0].State != "failure" || got.Checks[1].State != "success" || got.Checks[2].State != "failure" {
		t.Fatalf("states=%+v", got.Checks)
	}
	if got.Checks[0].FailureExcerpt != "pkg/TestBroken\n\nassertion failed" {
		t.Fatalf("excerpt=%q", got.Checks[0].FailureExcerpt)
	}
	if got.CITestReportNote != "Slow tests over the 10s budget: pkg/TestSlow (12s)" {
		t.Fatalf("slow tests=%q", got.CITestReportNote)
	}
	data, err := json.Marshal(got.Checks[0])
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]any
	if err = json.Unmarshal(data, &check); err != nil {
		t.Fatal(err)
	}
	if check["durationMs"] != float64(1250) {
		t.Fatalf("serialized check=%s", data)
	}
}

func TestLocalCIChecksLeaveGitHubAndOtherHeadsUnchanged(t *testing.T) {
	record := savedLocalCI(t, localci.Result{Status: "passed", Suites: []localci.SuiteResult{{Suite: "go", Status: "passed"}}})
	record.Checks = []watch.CheckSummary{{Name: "existing", State: "pending"}}
	for _, tc := range []struct{ name, kind, head string }{{"github", "github", record.Head}, {"other-head", "local", strings.Repeat("b", 40)}} {
		t.Run(tc.name, func(t *testing.T) {
			record.Kind = tc.kind
			record.Head = tc.head
			got := localCIChecks(watch.ReviewFacts{}, record)
			if len(got.Checks) != 1 || got.Checks[0].Name != "existing" {
				t.Fatalf("changed unrelated checks: %+v", got.Checks)
			}
		})
	}
}

func TestChangeViewReadsLocalCISuites(t *testing.T) {
	record := savedLocalCI(t, localci.Result{Status: "passed", Suites: []localci.SuiteResult{{Suite: "relay", Status: "passed", Duration: 750 * time.Millisecond}}})
	got := changeView(watch.ReviewFacts{}, record)
	if len(got.Checks) != 1 || got.Checks[0].Name != "relay" || got.Checks[0].State != "success" {
		t.Fatalf("local CI checks missing from change view: %+v", got.Checks)
	}
}

func TestChangeViewShowsSavedLocalCIRunErrors(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		suites        []localci.SuiteResult
	}{
		{name: "setup", message: "initialize cache: SSH connection refused"},
		{name: "cleanup", message: "cleanup: worktree removal failed", suites: []localci.SuiteResult{{Suite: "go", Status: "passed", Duration: time.Second}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := savedLocalCI(t, localci.Result{Status: "error", Error: tc.message, Duration: 2 * time.Second, Suites: tc.suites})
			got := changeView(watch.ReviewFacts{}, record)
			if len(got.Checks) != len(tc.suites)+1 {
				t.Fatalf("run error hidden: %+v", got.Checks)
			}
			check := got.Checks[len(got.Checks)-1]
			if check.Name != "local CI" || check.State != "failure" || check.Status != "completed" || check.Conclusion != "failure" || check.FailureExcerpt != tc.message || !check.FailureExcerptChecked || check.DurationMs != 2000 {
				t.Fatalf("run error check=%+v", check)
			}
			if len(tc.suites) > 0 && got.Checks[0].State != "success" {
				t.Fatalf("suite result lost: %+v", got.Checks)
			}
		})
	}
}
