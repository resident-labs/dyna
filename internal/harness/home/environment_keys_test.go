package home

import (
	"encoding/json"
	"testing"
)

func TestPrivateEnvironmentKeysCoverPreparedOverrides(t *testing.T) {
	scope, _, inherited := fixture(t)
	prepared, err := Prepare(scope, "/fake/dyna", inherited)
	if err != nil {
		t.Fatal(err)
	}
	for key := range prepared {
		if !PrivateEnvironmentKey(key) {
			t.Errorf("Prepare override %s is not protected from MCP references", key)
		}
	}
	for key := range Discovery(scope) {
		if !PrivateEnvironmentKey(key) {
			t.Errorf("discovery root %s is not protected from MCP references", key)
		}
	}
	if PrivateEnvironmentKey("UNRELATED_SERVER_TOKEN") {
		t.Fatal("ordinary server references should keep their original names")
	}
}

// OpenCode rejects an empty OPENCODE_CONFIG_CONTENT as malformed on every start.
func TestDiscoveryClearsOpenCodeConfigContentWithValidJSON(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(Discovery("/scope")["OPENCODE_CONFIG_CONTENT"]), &doc); err != nil || len(doc) != 0 {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not an empty JSON object: %v", err)
	}
}
