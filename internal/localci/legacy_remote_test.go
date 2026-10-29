package localci

import (
	"testing"

	"dyna-agent/internal/machines"
)

func TestLegacyRemoteNodeSelectsRegisteredMachine(t *testing.T) {
	nodes := []Node{
		{Name: "other", SSH: "ci@other", Enabled: true, Labels: []string{machines.LabelCI}},
		{Name: "hermeslab", SSH: "pip@hermeslab", Enabled: true, Labels: []string{machines.LabelCI}},
		{Name: "standby", SSH: "pip@hermeslab", Enabled: false, Labels: []string{machines.LabelCI}},
	}
	node, err := legacyRemoteNode("pip@hermeslab:/old/cache", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if node.Name != "hermeslab" || node.SSH != "pip@hermeslab" {
		t.Fatalf("legacy remote replaced registered machine settings: %+v", node)
	}
	if _, err := legacyRemoteNode("nobody@missing:/old/cache", nodes); err == nil {
		t.Fatal("legacy remote matched no enabled machine but did not fail")
	}
	nodes = append(nodes, Node{Name: "duplicate", SSH: "pip@hermeslab", Enabled: true, Labels: []string{machines.LabelCI}})
	if _, err := legacyRemoteNode("pip@hermeslab:/old/cache", nodes); err == nil {
		t.Fatal("ambiguous legacy remote did not fail")
	}
}
