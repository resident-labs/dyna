package localci

import (
	"testing"

	"github.com/goccy/go-yaml"
)

// TestTagWorkflowBodyNamesEveryForm covers the name forms a valid workflow can
// carry. The tagged copy must parse as one document whose single name is the
// tag, with the rest of the document intact: a line edit corrupts quoted keys,
// block scalars and a value on the line after its key, so the rewrite works on
// the parsed document.
func TestTagWorkflowBodyNamesEveryForm(t *testing.T) {
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
		{"value on the next line", "name:\n  CI\non: push\n"},
		{"name after other keys", "on: push\nname: CI\n"},
		{"document marker", "---\nname: CI\non: push\n"},
		{"no name", "on: push\n"},
		{"marker then no name", "---\non: push\n"},
		{"comment before the marker", "# comment\n---\non: push\njobs:\n  a: {runs-on: x}\n"},
		{"comment only before the keys", "# comment\non: push\n"},
		{"indented keys", "  on: push\n  jobs:\n    a: {runs-on: x}\n"},
		{"indented keys with a name", "  name: CI\n  on: push\n"},
		{"anchored name with an alias", "name: &ci CI\non: push\njobs:\n  build:\n    name: *ci\n    runs-on: x\n"},
		{"alias in a sequence", "name: &ci CI\non: push\nenv: [*ci, *ci]\n"},
		{"anchored block scalar name with an alias", "name: &ci |-\n  CI\non: push\njobs:\n  build:\n    name: *ci\n    runs-on: x\n"},
		{"flow mapping", "{on: push, jobs: {a: {runs-on: x}}}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tagWorkflowBody([]byte(tc.in), "CI-dyna-run-7-go-static")
			if err != nil {
				t.Fatalf("tag: %v", err)
			}
			// Arbitrary workflow YAML has no known shape, so the document is
			// read as a generic mapping; a duplicate key is a parse error.
			var doc map[string]any
			if err := yaml.Unmarshal(body, &doc); err != nil {
				t.Fatalf("the tagged copy is not valid YAML: %v, from %q to %q", err, tc.in, body)
			}
			if doc["name"] != "CI-dyna-run-7-go-static" {
				t.Fatalf("name = %v, want the tag, from %q to %q", doc["name"], tc.in, body)
			}
			if _, ok := doc["on"]; !ok {
				t.Fatalf("the on key was lost: %q -> %q", tc.in, body)
			}
		})
	}
}

func TestTagWorkflowBodyRejectsWhatIsNotAWorkflow(t *testing.T) {
	for _, in := range []string{"- a\n- b\n", "a: [\n"} {
		if body, err := tagWorkflowBody([]byte(in), "tag"); err == nil {
			t.Fatalf("tagged %q into %q, want an error", in, body)
		}
	}
	if body, err := tagWorkflowBody([]byte("name: A\n---\nname: B\n"), "tag"); err == nil {
		t.Fatalf("tagged two documents into %q", body)
	}
}

// TestTagWorkflowBodyKeepsAliasesOfTheOldName checks that replacing an
// anchored name leaves every field that aliased it with the original value, and
// that no alias is left pointing at an anchor that is gone.
func TestTagWorkflowBodyKeepsAliasesOfTheOldName(t *testing.T) {
	in := "name: &ci CI\non: push\nenv: [*ci, *ci]\njobs:\n  build:\n    name: *ci\n    runs-on: x\n"
	body, err := tagWorkflowBody([]byte(in), "CI-dyna-run-7-go-static")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name string   `yaml:"name"`
		Env  []string `yaml:"env"`
		Jobs map[string]struct {
			Name string `yaml:"name"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the tagged copy does not decode: %v: %q", err, body)
	}
	if doc.Name != "CI-dyna-run-7-go-static" || doc.Jobs["build"].Name != "CI" || len(doc.Env) != 2 || doc.Env[0] != "CI" || doc.Env[1] != "CI" {
		t.Fatalf("name %q, job name %q, env %v, want the tag and the old value twice: %q", doc.Name, doc.Jobs["build"].Name, doc.Env, body)
	}
}

func TestTagWorkflowBodyKeepsABlockScalarNameForItsAliases(t *testing.T) {
	in := "name: &ci |-\n  CI\non: push\njobs:\n  build:\n    name: *ci\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ok\n"
	body, err := tagWorkflowBody([]byte(in), "CI-dyna-run-7-go-static")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name string `yaml:"name"`
		Jobs map[string]struct {
			Name   string `yaml:"name"`
			RunsOn string `yaml:"runs-on"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the tagged copy does not decode: %v: %q", err, body)
	}
	if doc.Name != "CI-dyna-run-7-go-static" || doc.Jobs["build"].Name != "CI" || doc.Jobs["build"].RunsOn != "ubuntu-latest" {
		t.Fatalf("name %q, job %+v, want the tag and the old value: %q", doc.Name, doc.Jobs["build"], body)
	}
}
