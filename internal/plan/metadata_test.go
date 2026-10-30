package plan

import (
	"reflect"
	"testing"
)

func TestMetadataUsesTopLevelDeclaration(t *testing.T) {
	for _, tail := range []string{"", "return await agent('task')"} {
		src := `// name: 'comment'
const other = { name: 'other' }
function nested() { const meta = { name: 'nested', phases: [{title: 'wrong'}] } }
export const meta = {
  'name': 'actual',
  description: 'A } brace and an escaped \'quote\'.',
  phases: [{title: 'Build'}, {title: dynamic}, {title: 'Check'}]
}
` + tail
		m, err := ReadMetadata(src)
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "actual" || m.Description != "A } brace and an escaped 'quote'." || len(m.Phases) != 2 {
			t.Fatalf("metadata = %#v", m)
		}
		p := Extract(src, Options{})
		if p.Workflows[0].Label != m.Name || len(p.Phases) != len(m.Phases) {
			t.Fatalf("plan metadata differs: %#v", p)
		}
	}
}

func TestReadMetadataRejectsInvalidSource(t *testing.T) {
	if _, err := ReadMetadata("export const meta = {"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestReadMetadataDeterministicOptIn(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want bool
	}{
		{`const meta = { deterministic: true }; return 1`, true},
		{`const meta = { deterministic: false }; return 1`, false},
		{`const meta = { deterministic: 'true' }; return 1`, false},
		{`return 1`, false},
	} {
		m, err := ReadMetadata(tc.src)
		if err != nil {
			t.Fatal(err)
		}
		if m.Deterministic != tc.want {
			t.Fatalf("%s: deterministic = %v", tc.src, m.Deterministic)
		}
	}
}

func TestReadMetadataArgs(t *testing.T) {
	m, err := ReadMetadata(`export const meta = { args: [{name: 'question', required: true, description: 'Ask'}, {name: 'limit', required: false, description: 'Bound'}] }`)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkflowArg{{Name: "question", Required: true, Description: "Ask"}, {Name: "limit", Description: "Bound"}}
	if !reflect.DeepEqual(m.Args, want) {
		t.Fatalf("args = %#v, want %#v", m.Args, want)

	}
}
