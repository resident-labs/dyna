package roomsvc

import (
	"bytes"
	"os"
	"testing"
)

func TestMachineTypesFileMatchesGo(t *testing.T) {
	const output = "web/src/lib/api/machine.gen.ts"
	const filename = "../../" + output
	const update = "DYNA_UPDATE_MACHINE_TYPES=1 go test ./internal/roomsvc/ -run MachineTypes"
	want, err := generateTypes(output, update)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DYNA_UPDATE_MACHINE_TYPES") != "" {
		if err := os.WriteFile(filename, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run %s (read error: %v)", filename, update, err)
	}
}
