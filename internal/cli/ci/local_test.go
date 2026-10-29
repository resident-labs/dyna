package ci

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

func TestFindNodeSelectsMachines(t *testing.T) {
	nodes := []localci.Node{
		{Name: "this-box", Enabled: true},
		{Name: "hermes", SSH: "pip@hermeslab", Enabled: true},
		{Name: "paused", SSH: "ari@paused", Enabled: false},
	}
	for _, tc := range []struct {
		name string
		want string
		ok   bool
	}{
		{"hermes", "hermes", true},
		{"local", "this-box", true},
		{"paused", "paused", false},
		{"missing", "", false},
	} {
		node, ok := findNode(nodes, tc.name)
		if ok != tc.ok || node.Name != tc.want {
			t.Errorf("findNode(%q) = %q, %t", tc.name, node.Name, ok)
		}
	}
	if _, ok := findNode([]localci.Node{{Name: "this-box", Enabled: false}}, "local"); ok {
		t.Fatal("findNode used a disabled local machine")
	}
}

func TestDefaultNodeUsesTheOnlyEnabledMachine(t *testing.T) {
	one := []localci.Node{{Name: "this-box", Enabled: true}, {Name: "paused", SSH: "ari@paused"}}
	if node, err := defaultNode(one); err != nil || node.Name != "this-box" {
		t.Fatalf("defaultNode(one) = %q, %v", node.Name, err)
	}
	if _, err := defaultNode([]localci.Node{{Name: "paused", SSH: "ari@paused"}}); err == nil {
		t.Fatal("defaultNode accepted a registry with no enabled machine")
	}
	two := []localci.Node{{Name: "hermes", SSH: "pip@hermeslab", Enabled: true}, {Name: "builder", SSH: "ari@builder", Enabled: true}}
	node, err := defaultNode(two)
	if err == nil || node.Name != "" {
		t.Fatalf("defaultNode(two) = %q, %v; want a request for --node", node.Name, err)
	}
	if want := "builder, hermes"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the machines %q", err, want)
	}
}

func TestLocalCICommandsRegistered(t *testing.T) {
	root := NewCommand()
	for _, name := range []string{"run", "show", "tests", "mode"} {
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

// The timing warning in a PR notice is bounded and says to run dyna git ci show
// for the rest, so show must print every slow test of a check.
func TestPrintLocalResultListsSlowTests(t *testing.T) {
	cmd := NewCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	result := localci.Result{SHA: "abc", Status: "passed", Duration: time.Second, Checks: []localci.SuiteResult{{
		CheckName: "go-test-shard (0)", Node: "pve", Status: "passed",
		SlowTests: []localci.TestTiming{{Name: "pkg.TestSlowOne", Duration: 3 * time.Second}, {Name: "pkg.TestSlowTwo", Duration: 4500 * time.Millisecond}},
	}}}
	if err := printResult(cmd, result, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slow tests:", "pkg.TestSlowOne ran for 3s", "pkg.TestSlowTwo ran for 4.5s"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, &output)
		}
	}
}
