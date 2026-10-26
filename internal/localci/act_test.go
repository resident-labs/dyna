package localci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestTagWorkflowNamesEveryForm covers the name forms a valid workflow can
// carry. The tagged copy must stay valid YAML whose name is the tag, with the
// rest of the document intact: a line edit corrupts quoted keys and block
// scalars, so the rewrite is structural.
func TestTagWorkflowNamesEveryForm(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain", "name: CI\non: push\n"},
		{"quoted key", "\"name\": CI\non: push\n"},
		{"single-quoted key", "'name': CI\non: push\n"},
		{"quoted value", "name: \"CI\"\non: push\n"},
		{"block scalar", "name: |\n  hello\n  world\non: push\n"},
		{"folded scalar", "name: >-\n  hello\non: push\n"},
		{"value folded over lines", "name: CI\n  more words\non: push\n"},
		{"name after other keys", "on: push\nname: CI\n"},
		{"no name", "on: push\n"},
		{"marker then no name", "---\non: push\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.yml")
			out := filepath.Join(dir, "out.yml")
			if err := os.WriteFile(in, []byte(tc.in), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("sh", "-c", tagWorkflowSnippet(in, out, "CI-dyna-run-7-go-static")).CombinedOutput(); err != nil {
				t.Fatalf("the snippet failed: %v, %s", err, out)
			}
			body, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("the tagged copy was not written: %v", err)
			}
			// Arbitrary workflow YAML has no known shape, so the document is
			// read as a generic mapping.
			var doc map[string]any
			if err := yaml.Unmarshal(body, &doc); err != nil {
				t.Fatalf("the tagged copy is not valid YAML: %v, from %q to %q", err, tc.in, body)
			}
			if doc["name"] != "CI-dyna-run-7-go-static" {
				t.Fatalf("name = %v, want the tag, from %q to %q", doc["name"], tc.in, body)
			}
			if strings.Contains(tc.in, "on:") {
				if _, ok := doc["on"]; !ok {
					t.Fatalf("the on key was lost: %q -> %q", tc.in, body)
				}
			}
		})
	}
}
