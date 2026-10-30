package roomsvc

import (
	"bytes"
	"os"
	"testing"
)

func TestFinalTypesFileMatchesGo(t *testing.T) {
	const update = "DYNA_UPDATE_FINAL_TYPES=1 go test ./internal/roomsvc/ -run FinalTypes"
	for _, output := range []string{
		"web/src/lib/api/connect.gen.ts",
		"web/src/lib/api/lanesync.gen.ts",
		"web/src/lib/api/voice.gen.ts",
		"web/src/lib/api/library.gen.ts",
	} {
		t.Run(output, func(t *testing.T) {
			want, err := generateTypes(output, update)
			if err != nil {
				t.Fatal(err)
			}
			filename := "../../" + output
			if os.Getenv("DYNA_UPDATE_FINAL_TYPES") != "" {
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
