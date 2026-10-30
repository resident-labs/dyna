package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTestSlotsSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, tc := range []struct {
		data    string
		valid   bool
		slots   int
		command string
	}{
		{`{"testSlots":{"slots":0,"commands":[{"command":"pytest","arg":""}]}}`, true, 0, "pytest"},
		{`{"testSlots":{"slots":-1}}`, false, 0, ""},
		{`{"testSlots":{"slots":null}}`, false, 0, ""},
		{`{"testSlots":{"commands":[{"command":"../go","arg":"test"}]}}`, false, 0, ""},
		{`{"testSlots":{"commands":[{"command":"go"}]}}`, false, 0, ""},
		{`{"testSlots":{"commands":[{"command":"go","arg":"test"},{"command":"go","arg":"test"}]}}`, false, 0, ""},
	} {
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		v, err := Load(path)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.data, err)
		}
		if tc.valid && (v.TestSlots.Slots != tc.slots || len(v.TestSlots.Commands) != 1 || v.TestSlots.Commands[0].Command != tc.command) {
			t.Fatalf("%s: %+v", tc.data, v.TestSlots)
		}
	}
}
