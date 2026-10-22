package plan

import (
	"fmt"
	"sort"
	"strings"
)

// MermaidView selects which projection to render.
type MermaidView string

const (
	MermaidCombined MermaidView = "combined"
	MermaidPlanned  MermaidView = "planned"
	MermaidActual   MermaidView = "actual"
)

// RenderMermaid returns deterministic, escaped, size-capped flowchart source.
func RenderMermaid(p *Plan, rec *Reconciliation, view MermaidView) string {
	if view == "" {
		view = MermaidCombined
	}
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	if p == nil || p.Status == StatusUnavailable {
		b.WriteString("  unavailable[\"plan unavailable\"]\n")
		writeLegend(&b, true, false, p)
		return b.String()
	}

	stateByNode := map[string][]string{}
	if rec != nil {
		for _, e := range rec.Entries {
			if e.PlanNodeID != "" {
				stateByNode[e.PlanNodeID] = append(stateByNode[e.PlanNodeID], e.State)
			}
		}
	}

	nodes, truncated := capMermaidNodes(p.Nodes)
	edges, edgeTrunc := capMermaidEdges(p.Edges, nodes)
	truncated = truncated || edgeTrunc
	keep := map[string]bool{}
	for _, n := range nodes {
		keep[n.PlanNodeID] = true
	}

	for _, n := range nodes {
		if view == MermaidActual && !hasActual(stateByNode[n.PlanNodeID]) && n.Kind != KindWorkflow {
			continue
		}
		label := mermaidLabel(n, stateByNode[n.PlanNodeID], view)
		b.WriteString("  ")
		b.WriteString(safeID(n.PlanNodeID))
		b.WriteString("[\"")
		b.WriteString(label)
		b.WriteString("\"]\n")
	}
	for _, e := range edges {
		if !keep[e.From] || !keep[e.To] {
			continue
		}
		if view == MermaidActual && !hasActual(stateByNode[e.From]) && !hasActual(stateByNode[e.To]) {
			if e.From != RootWorkflowID && e.To != RootWorkflowID {
				continue
			}
		}
		b.WriteString("  ")
		b.WriteString(safeID(e.From))
		b.WriteString(" -->")
		if e.Kind != EdgeSequence && e.Kind != EdgeContains {
			b.WriteString("|")
			b.WriteString(escapeMermaid(e.Kind))
			b.WriteString("|")
		}
		b.WriteString(" ")
		b.WriteString(safeID(e.To))
		b.WriteString("\n")
	}
	writeLegend(&b, p.Status != StatusComplete, truncated, p)
	return b.String()
}

func capMermaidNodes(in []Node) ([]Node, bool) {
	var root *Node
	var rest []Node
	for i := range in {
		n := in[i]
		if n.Kind == KindLog {
			continue
		}
		if n.PlanNodeID == RootWorkflowID {
			cp := n
			root = &cp
			continue
		}
		rest = append(rest, n)
	}
	sort.Slice(rest, func(i, j int) bool {
		pi, pj := mermaidKeepPriority(rest[i]), mermaidKeepPriority(rest[j])
		if pi != pj {
			return pi < pj
		}
		return rest[i].PlanNodeID < rest[j].PlanNodeID
	})
	truncated := false
	maxRest := MaxGraphNodes
	if root != nil {
		maxRest = MaxGraphNodes - 1
	}
	if maxRest < 0 {
		maxRest = 0
	}
	if len(rest) > maxRest {
		rest = rest[:maxRest]
		truncated = true
	}
	out := rest
	if root != nil {
		out = append([]Node{*root}, rest...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlanNodeID < out[j].PlanNodeID })
	return out, truncated
}

func mermaidKeepPriority(n Node) int {
	switch n.Kind {
	case KindWorkflow, KindAgent, KindPhase, KindParallel, KindPipeline, KindBranch, KindLoop:
		return 0
	default:
		return 1
	}
}

func capMermaidEdges(in []Edge, nodes []Node) ([]Edge, bool) {
	keep := map[string]bool{}
	for _, n := range nodes {
		keep[n.PlanNodeID] = true
	}
	var kept []Edge
	for _, e := range in {
		if keep[e.From] && keep[e.To] {
			kept = append(kept, e)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].From != kept[j].From {
			return kept[i].From < kept[j].From
		}
		if kept[i].To != kept[j].To {
			return kept[i].To < kept[j].To
		}
		return kept[i].Kind < kept[j].Kind
	})
	if len(kept) <= MaxGraphEdges {
		return kept, false
	}
	var head, tail []Edge
	for _, e := range kept {
		if e.From == RootWorkflowID || e.To == RootWorkflowID {
			head = append(head, e)
		} else {
			tail = append(tail, e)
		}
	}
	remain := MaxGraphEdges - len(head)
	if remain < 0 {
		head = head[:MaxGraphEdges]
		remain = 0
	}
	if len(tail) > remain {
		tail = tail[:remain]
	}
	out := append(head, tail...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})
	return out, true
}

func hasActual(states []string) bool {
	for _, s := range states {
		switch s {
		case StateQueued, StateRunning, StateOK, StateError, StateCanceled, StateUnmatched, StateDynamic:
			return true
		}
	}
	return false
}

func mermaidLabel(n Node, states []string, view MermaidView) string {
	parts := []string{n.Kind}
	if n.Label != "" && n.Label != n.Kind {
		parts = append(parts, n.Label)
	}
	if n.Profile != "" {
		parts = append(parts, n.Profile)
	}
	if n.Certainty != CertaintyCertain {
		parts = append(parts, n.Certainty)
	}
	if n.Cardinality.Kind != CardExact {
		parts = append(parts, cardMark(n.Cardinality))
	}
	if view != MermaidPlanned && len(states) > 0 {
		parts = append(parts, strings.Join(unique(states), ","))
	}
	return escapeMermaid(strings.Join(parts, " · "))
}

func cardMark(c Cardinality) string {
	switch c.Kind {
	case CardExact:
		if c.Max != nil {
			return "x" + itoa(*c.Max)
		}
	case CardRange:
		return "~"
	}
	if c.Reason != "" {
		return "? " + c.Reason
	}
	return "?"
}

func writeLegend(b *strings.Builder, uncertain, truncated bool, p *Plan) {
	b.WriteString("  subgraph legend\n")
	b.WriteString("    L1[\"certain vs conditional/template/unknown\"]\n")
	b.WriteString("    L2[\"? / ~ / xN? = unknown cardinality — never an invented count\"]\n")
	if truncated {
		b.WriteString("    L3[\"graph truncated at node/edge cap\"]\n")
	}
	if p != nil && p.Status != StatusComplete {
		b.WriteString("    L4[\"plan status: ")
		b.WriteString(escapeMermaid(p.Status))
		b.WriteString("\"]\n")
	}
	if uncertain && p != nil && len(p.Diagnostics) > 0 {
		b.WriteString("    L5[\"")
		b.WriteString(escapeMermaid(p.Diagnostics[0].Message))
		b.WriteString("\"]\n")
	}
	b.WriteString("  end\n")
}

func escapeMermaid(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	repl := []struct{ old, new string }{
		{`"`, "#quot;"},
		{"[", "#91;"},
		{"]", "#93;"},
		{"{", "#123;"},
		{"}", "#125;"},
		{"|", "#124;"},
		{"<", "#lt;"},
		{">", "#gt;"},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}

func safeID(id string) string {
	if id == "" {
		return "n0"
	}
	var b strings.Builder
	for _, r := range id {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteString(fmt.Sprintf("_%d", r))
		}
	}
	return b.String()
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
