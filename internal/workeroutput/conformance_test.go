package workeroutput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conformanceFixtures lists captures per dialect. Multiple captures cover
// each stream feature without altering real records.
var conformanceFixtures = map[string][]string{
	DialectPiJSON:           {"pi-stream.jsonl"},
	DialectText:             {"plain-text.txt"},
	DialectCodexJSONL:       {"codex-tools.jsonl"},
	DialectClaudeStreamJSON: {"claude-tools.jsonl"},
	DialectOpenCodeJSON:     {"opencode-stream.jsonl"},
	DialectMuseJSONL:        {"muse-stream.jsonl"},
}

func fixtureLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestAdapterConformance(t *testing.T) {
	for dialect := range dialectCapabilities {
		if _, ok := adapters[dialect]; !ok {
			t.Errorf("capabilities for unregistered dialect %q", dialect)
		}
	}
	for dialect := range adapters {
		t.Run(dialect, func(t *testing.T) {
			paths, ok := conformanceFixtures[dialect]
			if !ok || len(paths) == 0 {
				t.Fatalf("new dialect %q requires conformance fixtures", dialect)
			}
			c, ok := dialectCapabilities[dialect]
			if !ok {
				t.Fatalf("new dialect %q requires capabilities", dialect)
			}
			decl := &Declaration{Dialect: dialect}
			n := NewUsageNormalizer("", nil)
			tracker := NewToolTracker(decl)
			completed := false
			transcript, tools, session, boundary := false, false, false, false
			var usage Usage
			for _, path := range paths {
				for _, line := range fixtureLines(t, path) {
					r := DecodeLine(decl, line)
					completed = tracker.ObserveSignals(r.Signals) || completed
					for _, e := range r.Events {
						transcript = transcript || (e.Kind == "message" && e.Role == "assistant" && e.Text != "")
						tools = tools || e.Kind == "tool"
					}
					session = session || r.SessionID != ""
					boundary = boundary || len(r.Signals) > 0
					if u, ok := n.ObserveRecord(r); ok {
						usage.Add(u)
					}
				}
			}
			if transcript != c.Transcript || tools != c.ToolEvents || session != c.SessionResume || boundary != c.SafePoints {
				t.Fatalf("fixtures: transcript=%v toolEvents=%v sessionResume=%v safePoints=%v; capabilities=%+v", transcript, tools, session, boundary, c)
			}
			if c.SafePoints && !completed {
				t.Fatal("fixtures do not demonstrate a safe completion")
			}
			if SupportsToolBoundaries(decl) != boundary {
				t.Fatal("boundary support differs from fixtures")
			}
			switch c.Usage {
			case "yes":
				if usage.Total() <= 0 || c.UsageReason != "" {
					t.Fatalf("usage=%+v capabilities=%+v", usage, c)
				}
			case "unavailable", "no":
				if usage.Total() != 0 || c.UsageReason == "" || !strings.Contains(n.Normalization().Reason, c.UsageReason) {
					t.Fatalf("missing unavailable-usage evidence: usage=%+v normalization=%+v capabilities=%+v", usage, n.Normalization(), c)
				}
			default:
				t.Fatalf("invalid usage capability %q", c.Usage)
			}
		})
	}
}

func TestUnknownCapabilities(t *testing.T) {
	c := Capabilities("future-json")
	if c.Transcript || c.ToolEvents || c.SafePoints || c.SessionResume || c.Usage != "no" || c.UsageReason == "" {
		t.Fatalf("unknown capabilities = %+v", c)
	}
	if Capabilities(" "+DialectMuseJSONL+" ") != Capabilities(DialectMuseJSONL) {
		t.Fatal("dialect spaces not trimmed")
	}
}

// TestReasoningConformance checks docs/reasoning-and-compaction.md for every
// adapter: readable reasoning has one shape, and hidden or empty reasoning
// emits nothing.
func TestReasoningConformance(t *testing.T) {
	cases := map[string]struct {
		readable, state string
		hidden          []string
	}{
		DialectClaudeStreamJSON: {`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Plan.","signature":"s"}]}}`, "completed", []string{
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"s"}]}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"redacted_thinking","data":"x"}]}}`,
		}},
		DialectCodexJSONL:   {`{"type":"item.completed","item":{"id":"i","type":"reasoning","text":"Plan."}}`, "completed", []string{`{"type":"item.completed","item":{"id":"i","type":"reasoning","text":""}}`}},
		DialectOpenCodeJSON: {`{"type":"reasoning","part":{"type":"reasoning","text":"Plan."}}`, "completed", []string{`{"type":"reasoning","part":{"type":"reasoning","text":""}}`}},
		DialectPiJSON: {`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Plan."}]}}`, "completed", []string{
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","thinkingSignature":"s","redacted":true}]}}`,
		}},
	}
	for dialect := range adapters {
		t.Run(dialect, func(t *testing.T) {
			decl := &Declaration{Dialect: dialect}
			c, ok := cases[dialect]
			if !ok {
				// No reasoning source: no fixture line may produce reasoning.
				for _, path := range conformanceFixtures[dialect] {
					for _, line := range fixtureLines(t, path) {
						for _, e := range DecodeLine(decl, line).Events {
							if e.Kind == "reasoning" {
								t.Fatalf("unexpected reasoning in %s: %+v", path, e)
							}
						}
					}
				}
				return
			}
			got := DecodeLine(decl, c.readable).Events
			if len(got) != 1 || got[0] != (Event{V: SchemaVersion, Kind: "reasoning", State: c.state, Text: "Plan."}) {
				t.Fatalf("readable = %+v", got)
			}
			for _, line := range c.hidden {
				for _, e := range DecodeLine(decl, line).Events {
					if e.Kind == "reasoning" {
						t.Errorf("hidden %s produced %+v", line, e)
					}
				}
			}
		})
	}
}
