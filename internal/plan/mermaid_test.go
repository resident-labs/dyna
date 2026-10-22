package plan

import (
	"strings"
	"testing"
)

func TestMermaidDeterministicEscapedAndCapped(t *testing.T) {
	src := `phase('P')
await agent('say "hi" [x]', { label: 'a|b' })
if (args.x) { await agent('maybe') }
return 1
`
	p := Extract(src, Options{})
	rec := Reconcile(p, nil, false)
	a := RenderMermaid(p, rec, MermaidCombined)
	b := RenderMermaid(p, rec, MermaidCombined)
	if a != b {
		t.Fatal("mermaid is not deterministic")
	}
	if !strings.HasPrefix(a, "flowchart TD\n") {
		t.Fatalf("prefix = %q", a[:min(40, len(a))])
	}
	if strings.Contains(a, "a|b") || strings.Contains(a, "[x]") {
		t.Fatalf("unescaped label:\n%s", a)
	}
	if !strings.Contains(a, "#124;") || !strings.Contains(a, "legend") {
		t.Fatalf("missing escape or legend:\n%s", a)
	}
	if !strings.Contains(a, "agent") || !strings.Contains(a, "phase") {
		t.Fatalf("missing kinds:\n%s", a)
	}
	planned := RenderMermaid(p, rec, MermaidPlanned)
	if planned == "" || !strings.Contains(planned, "legend") {
		t.Fatalf("planned view empty")
	}
}

func TestMermaidUnavailableAndTruncationLegend(t *testing.T) {
	u := Unavailable("function (", Options{}, "bad syntax")
	out := RenderMermaid(u, nil, MermaidCombined)
	if !strings.Contains(out, "plan unavailable") || !strings.Contains(out, "legend") {
		t.Fatalf("unavailable mermaid:\n%s", out)
	}
}

func TestMermaidKeepsRootWhenCappedAndSkipsLogs(t *testing.T) {
	var b strings.Builder
	b.WriteString("export const meta = { name: 'cap' }\n")
	for i := 0; i < 90; i++ {
		b.WriteString("log('line " + itoa(i) + "')\n")
		b.WriteString("await agent('a" + itoa(i) + "', { label: 'a" + itoa(i) + "' })\n")
	}
	b.WriteString("return 1\n")
	p := Extract(b.String(), Options{})
	if p.Counts.Nodes < 80 {
		t.Fatalf("expected a large graph, got %#v", p.Counts)
	}
	out := RenderMermaid(p, nil, MermaidCombined)
	if !strings.Contains(out, " "+RootWorkflowID+"[") && !strings.Contains(out, "\n  "+RootWorkflowID+"[") {
		t.Fatalf("capped mermaid dropped root wf:\n%s", out)
	}
	if strings.Contains(out, "log ·") {
		t.Fatalf("mermaid rendered log nodes:\n%s", out)
	}
	if !strings.Contains(out, "agent") {
		t.Fatalf("capped mermaid omitted agents:\n%s", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation legend:\n%s", out)
	}
}
