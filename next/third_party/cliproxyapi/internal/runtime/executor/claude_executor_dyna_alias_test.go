package executor

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

// Pi answered "Tool ... not found" to these three shapes of drift on live Opus turns, for example mcp__human_write,
// mcp__mcp__device_juice__spirit_write and mcp__dyna__dilemma_edit, and later maid_bash with mcp__ dropped as well.
func TestClaudeMCPAliasRestoresALostServer(t *testing.T) {
	body := []byte(`{"tools":[{"name":"read","input_schema":{"type":"object"}},{"name":"write","input_schema":{"type":"object"}},{"name":"mcp__dyna__session","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "lost-server-caller"})
	alias := gjson.GetBytes(remapped, "tools.1.name").String()
	parts, ok := parseClaudeMCPAlias(alias)
	if !ok {
		t.Fatalf("alias %q does not parse", alias)
	}
	tool := parts.toolID + "_" + parts.semantic

	for _, drifted := range []string{"mcp__" + tool, "mcp__" + alias, "mcp__dyna__" + tool, tool} {
		response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, drifted))
		restored, err := reverseRemapOAuthToolNames(response, reverseMap)
		if err != nil {
			t.Fatalf("%s: %v", drifted, err)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != "write" {
			t.Errorf("%s restored as %q", drifted, got)
		}
	}

	// The tool ID is the evidence: a shared suffix, or a name the caller declared, stays as it is.
	resolver := newClaudeMCPAliasResolver(reverseMap)
	for _, other := range []string{"mcp__dyna__session", "mcp__other_write", "mcp__dyna__other_write", "write"} {
		if got, matched, err := resolver.resolve(other); matched || err != nil {
			t.Errorf("%s resolved as %q, %v", other, got, err)
		}
	}
}
