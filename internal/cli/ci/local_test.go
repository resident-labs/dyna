package ci

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

func TestLocalCICommandsRegistered(t *testing.T) {
	root := NewCommand()
	for _, name := range []string{"run", "show", "tests"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}
func TestPrintLocalResultJSON(t *testing.T) {
	cmd := NewCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	result := localci.Result{SHA: "abc", Status: "failed", Duration: time.Second, Suites: []localci.SuiteResult{{Suite: "go", Status: "failed", Failures: []string{"pkg/TestBroken"}}}}
	if err := printResult(cmd, result, true); err != nil {
		t.Fatal(err)
	}
	var decoded localci.Result
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "failed" || decoded.Suites[0].Failures[0] != "pkg/TestBroken" {
		t.Fatalf("JSON=%s", &output)
	}
}
