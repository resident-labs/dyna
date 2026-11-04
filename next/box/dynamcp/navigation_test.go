package dynamcp

import (
	"strings"
	"testing"
)

// Each navigation tool's description says what it can reach, in one place; the agent learns it nowhere else.
func TestNavigationToolDescriptionsSayWhatTheyReach(t *testing.T) {
	list, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"search":     {"across rooms", "a DM post is found only in your own room"},
		"history":    {"any room's feed", "DM", "your own room only"},
		"transcript": {"Only sessions of your own room", "another room's transcript is refused"},
	}
	for _, tool := range list {
		for _, phrase := range want[tool.Name] {
			if !strings.Contains(tool.Description, phrase) {
				t.Errorf("%s description lacks %q: %s", tool.Name, phrase, tool.Description)
			}
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("tools missing: %v", want)
	}
}
