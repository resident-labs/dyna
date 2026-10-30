package roomsvc

import (
	"bytes"
	"encoding/json"

	"dyna-agent/internal/sysload"
	"dyna-agent/internal/terminalsvc"
	"os"
	"testing"
)

// TestMiscTypesFileMatchesGo uses the shared tygo setup for these wire contracts.
func TestMiscTypesFileMatchesGo(t *testing.T) {
	const update = "DYNA_UPDATE_MISC_TYPES=1 go test ./internal/roomsvc/ -run MiscTypes"
	for _, output := range []string{
		"web/src/lib/api/profile.gen.ts",
		"web/src/lib/api/system-state.gen.ts",
		"web/src/lib/api/system-hub.gen.ts",
		"web/src/lib/api/system.gen.ts",
		"web/src/lib/api/computer.gen.ts",
		"web/src/lib/api/terminal.gen.ts",
	} {
		t.Run(output, func(t *testing.T) {
			want, err := generateTypes(output, update)
			if err != nil {
				t.Fatal(err)
			}
			filename := "../../" + output
			if os.Getenv("DYNA_UPDATE_MISC_TYPES") != "" {
				if err := os.WriteFile(filename, want, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s is out of date; run %s (read error: %v)", filename, update, err)
			}
		})
	}
}

// Nil slices and pointers stay present in these wire responses.
func TestMiscTypesNullJSON(t *testing.T) {
	job, err := json.Marshal(terminalsvc.ShellJob{})
	if err != nil {
		t.Fatal(err)
	}
	load, err := json.Marshal(sysload.Load{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		data []byte
		want string
	}{
		{job, `"command":null`}, {load, `"cpuPercent":null`}, {load, `"split":null`},
	} {
		if !bytes.Contains(test.data, []byte(test.want)) {
			t.Errorf("missing %s in %s", test.want, test.data)
		}
	}
}
