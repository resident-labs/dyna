package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestEveryHarnessRegistryEntryConforms(t *testing.T) {
	for _, name := range profile.Harnesses {
		t.Run(name, func(t *testing.T) {
			d, ok := profile.HarnessDefinitionFor(name)
			if !ok || d.Dialect == "" || d.Invocation == "" || d.ProbePrompt == "" || len(d.Fixtures) == 0 {
				t.Fatalf("incomplete harness registry entry: %#v", d)
			}
			if name != profile.HarnessCustom && name != profile.HarnessMock && (d.Binary == "" || len(d.VersionArgs) == 0) {
				t.Fatal("native harness lacks executable or version detection")
			}
			p := profile.Profile{Harness: name, Command: []string{"true"}}
			if name != profile.HarnessMock {
				invocation, err := buildInvocation(p, "probe")
				if err != nil {
					t.Fatal(err)
				}
				if invocation.cleanup != nil {
					defer invocation.cleanup()
				}
				if d.Binary != "" && len(invocation.initial.argv) > 0 && invocation.initial.argv[0] != d.Binary {
					t.Fatalf("launcher does not use registry binary: %v", invocation.initial.argv)
				}
				// An ACP harness launches no CLI from its invocation; its prompt
				// rides the account's long-lived agent process instead.
				if len(invocation.initial.argv) == 0 && invocation.initial.acpPrompt == "" {
					t.Fatal("invocation has neither argv nor an ACP prompt")
				}
				if (invocation.resume != nil) != (d.Resume != "") {
					t.Fatalf("resume contract differs: %#v", d)
				}
			}
			decl := p.EffectiveOutput()
			tracker := workeroutput.NewToolTracker(decl)
			normalizer := workeroutput.NewUsageNormalizer("", nil)
			text, tool, session, completed := false, false, false, false
			var usage workeroutput.Usage
			for _, fixture := range d.Fixtures {
				data, err := os.ReadFile(filepath.Join("..", "workeroutput", "testdata", fixture))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					r := workeroutput.DecodeLine(decl, line)
					session = session || r.SessionID != ""
					completed = tracker.ObserveSignals(r.Signals) || completed
					for _, e := range r.Events {
						text = text || e.Kind == "message" && e.Role == "assistant" && e.Text != ""
						tool = tool || e.Kind == "tool"
					}
					if u, ok := normalizer.ObserveRecord(r); ok {
						usage.Add(u)
					}
				}
			}
			c := d.Capabilities()
			if text != c.Transcript || tool != c.ToolEvents || session != c.SessionResume || completed != c.SafePoints || (usage.Total() > 0) != (c.Usage == "yes") {
				t.Fatalf("registry capabilities disagree with fixtures: %#v; text=%t tool=%t session=%t completed=%t usage=%d", c, text, tool, session, completed, usage.Total())
			}
		})
	}
}
