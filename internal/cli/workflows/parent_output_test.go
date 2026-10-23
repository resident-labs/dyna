package workflows

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestRunPrintsReferencesButKeepsFullHistory(t *testing.T) {
	inRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	serveWorkflowEngine(t)
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(profile.Profile{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "wf.js")
	// The script can still read the full string before it returns.
	if err := os.WriteFile(script, []byte(`const text = "界".repeat(1501); return {text, length: text.length}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, envelope := range []bool{false, true} {
		cmd := NewRunCommand()
		args := []string{script, "--quiet", "--dir", dir}
		if envelope {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		var output bytes.Buffer
		cmd.SetOut(&output)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		printed := output.Bytes()
		var value json.RawMessage = printed
		if envelope {
			var wrapped struct{ Result json.RawMessage }
			if err := json.Unmarshal(printed, &wrapped); err != nil {
				t.Fatal(err)
			}
			value = wrapped.Result
		}
		var got struct {
			Text   parentoutput.Reference
			Length int
		}
		if err := json.Unmarshal(value, &got); err != nil {
			t.Fatalf("printed result = %s: %v", printed, err)
		}
		full := strings.Repeat("界", 1501)
		saved, err := os.ReadFile(got.Text.File)
		if err != nil || string(saved) != full || got.Text.Chars != 1501 || got.Length != 1501 || got.Text.Preview != strings.Repeat("界", 300) {
			t.Fatalf("reference = %+v, error = %v", got, err)
		}
		id := filepath.Base(filepath.Dir(filepath.Dir(got.Text.File)))
		history, ok := runstore.ReadResult(id)
		var stored struct{ Text string }
		if !ok || json.Unmarshal([]byte(history), &stored) != nil || stored.Text != full {
			t.Fatalf("history = %s", history)
		}
	}
}
