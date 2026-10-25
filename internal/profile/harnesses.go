package profile

import (
	"slices"

	"dyna-agent/internal/workeroutput"
)

// HarnessDefinition is the native contract shared by launchers, profile checks,
// health reports, and configuration views. Invocation and Resume describe the
// command strategies implemented by internal/harness.
type HarnessDefinition struct {
	Name        string   `json:"name"`
	Binary      string   `json:"binary"`
	VersionArgs []string `json:"versionArgs"`
	Dialect     string   `json:"dialect"`
	Invocation  string   `json:"invocation"`
	Resume      string   `json:"resume"`
	ProbePrompt string   `json:"probePrompt"`
	Fixtures    []string `json:"-"`
}

const HarnessProbeReply = "DYNA_PROFILE_CHECK_OK"
const harnessProbePrompt = "Run this tiny shell command once: printf 'DYNA_PROFILE_TOOL_OK\\n'. Then reply with exactly DYNA_PROFILE_CHECK_OK. Do not edit files or run other commands."

// This is the only harness metadata table. Vendor record decoding stays in
// workeroutput, whose conformance suite verifies the dialect capabilities.
var harnessDefinitions = []HarnessDefinition{
	{HarnessClaudeCode, "claude", []string{"--version"}, workeroutput.DialectClaudeStreamJSON, "claude-print", "--resume", harnessProbePrompt, []string{"claude-tools.jsonl"}},
	{HarnessOpenCode, "opencode", []string{"--version"}, workeroutput.DialectOpenCodeJSON, "opencode-run", "--session", harnessProbePrompt, []string{"opencode-stream.jsonl"}},
	{HarnessMuse, "muse", []string{"--version"}, workeroutput.DialectMuseJSONL, "muse-exec", "--session-id", harnessProbePrompt, []string{"muse-stream.jsonl"}},
	{HarnessPi, "pi", []string{"--version"}, workeroutput.DialectPiJSON, "pi-print", "--session", harnessProbePrompt, []string{"pi-stream.jsonl"}},
	{HarnessMock, "", nil, workeroutput.DialectText, "mock", "mock", harnessProbePrompt, []string{"plain-text.txt"}},
}

func (h HarnessDefinition) Capabilities() workeroutput.DialectCapabilities {
	return workeroutput.Capabilities(h.Dialect)
}

// HarnessDefinitionFor returns a copy so callers cannot change the registry.
func HarnessDefinitionFor(name string) (HarnessDefinition, bool) {
	for _, h := range harnessDefinitions {
		if h.Name == name {
			h.VersionArgs = slices.Clone(h.VersionArgs)
			h.Fixtures = slices.Clone(h.Fixtures)
			return h, true
		}
	}
	return HarnessDefinition{}, false
}

func harnessNames() []string {
	names := make([]string, 0, len(harnessDefinitions))
	for _, h := range harnessDefinitions {
		names = append(names, h.Name)
	}
	return names
}
