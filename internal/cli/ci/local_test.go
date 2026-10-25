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
		{Name: "this-box", Slots: 2, Enabled: true},
		{Name: "hermes", SSH: "pip@hermeslab", Slots: 2, Enabled: true},
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
	for _, name := range []string{"run", "show", "tests", "node", "mode"} {
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
