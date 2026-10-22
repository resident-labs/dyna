package profile

import (
	"strings"
	"testing"
)

func TestWorkflowProfileUsesRegisteredPolicy(t *testing.T) {
	s := &Store{Profiles: []Profile{
		{Name: "session", Default: true, Uses: []string{UseAgents}},
		{Name: "worker", Harness: HarnessCustom, Command: []string{"custom-worker"}, ExtraArgs: []string{"--registered"}},
		{Name: "disabled", Disabled: true},
	}}
	for _, name := range []string{"", "worker"} {
		p, err := s.WorkflowProfile(name)
		if err != nil || p != &s.Profiles[1] {
			t.Fatalf("WorkflowProfile(%q) = %#v, %v; want registered worker", name, p, err)
		}
		if p.Command[0] != "custom-worker" || p.ExtraArgs[0] != "--registered" {
			t.Fatal("selection replaced registered custom options")
		}
	}
	for _, tc := range []struct{ name, want string }{
		{"missing", "unknown profile"},
		{"disabled", "is disabled"},
		{"session", "is not enabled for workflows"},
	} {
		if _, err := s.WorkflowProfile(tc.name); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("WorkflowProfile(%q) = %v; want %q", tc.name, err, tc.want)
		}
	}
	if _, err := (&Store{}).WorkflowProfile(""); err == nil {
		t.Fatal("empty registry accepted a default")
	}
}
