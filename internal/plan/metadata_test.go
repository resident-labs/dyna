package plan

import "testing"

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
