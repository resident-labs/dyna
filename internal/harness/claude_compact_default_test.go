package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
)

func TestClaudeOwnerCompactCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cap     int64
		compact *profile.Compact
		native  string
		want    float64
	}{
		{"default cap overrides native", 200000, nil, `{"autoCompactWindow":400000}`, 233000},
		{"zero keeps native", 0, nil, `{"autoCompactWindow":400000}`, 400000},
		{"profile wins", 200000, &profile.Compact{Tokens: 95000}, `{"autoCompactWindow":400000}`, 128000},
		{"low cap clamps", 1, nil, `{}`, 100000},
		{"high cap clamps", 2000000, nil, `{}`, 1000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", config)
			if err := os.MkdirAll(filepath.Join(config, "dyna"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "dyna", "settings.json"), []byte(fmt.Sprintf(`{"harness":{"compactAtTokens":%d}}`, tc.cap)), 0600); err != nil {
				t.Fatal(err)
			}
			p := profile.Profile{Harness: profile.HarnessClaudeCode, Model: "haiku", Compact: tc.compact, ExtraArgs: []string{"--settings", tc.native}}
			var argv []string
			_, err := RunWithOptions(context.Background(), p, "task", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{
				ProcessRunner: func(_ context.Context, args []string, stdout, _ io.Writer) error {
					argv = args
					_, err := io.WriteString(stdout, `{"type":"result","subtype":"success","result":"ok"}`+"\n")
					return err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			var layer map[string]json.RawMessage
			for i := 0; i+1 < len(argv); i++ {
				if argv[i] == "--settings" {
					if err := json.Unmarshal([]byte(argv[i+1]), &layer); err != nil {
						t.Fatal(err)
					}
				}
			}
			var got float64
			if err := json.Unmarshal(layer["autoCompactWindow"], &got); err != nil || got != tc.want {
				t.Fatalf("window = %v, error = %v; want %v", got, err, tc.want)
			}
		})
	}
}
