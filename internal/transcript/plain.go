package transcript

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// PlainOptions tunes the text rendering used by the CLI.
type PlainOptions struct {
	Agent  string // label for replies (the profile)
	Expand bool   // include each tool call's command and output tail
	Now    time.Time
}

// WriteTurn writes one turn as readable text: a separator with status,
// duration, and tokens, then the conversation. Markdown is left as written.
func WriteTurn(w io.Writer, t Turn, o PlainOptions) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	var parts []string
	parts = append(parts, t.Status)
	if d := t.Duration(o.Now); d > 0 {
		parts = append(parts, compactDuration(d))
	}
	if tok := t.Usage.Total(); tok > 0 {
		parts = append(parts, fmt.Sprintf("%d tok", tok))
	}
	if t.Usage.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", t.Usage.CostUSD))
	}
	if !t.Started.IsZero() {
		parts = append(parts, t.Started.Local().Format("Jan 02 15:04"))
	}
	head := fmt.Sprintf("── turn %d · %s ", t.N, strings.Join(parts, " · "))
	if n := 78 - len([]rune(head)); n > 0 {
		head += strings.Repeat("─", n)
	}
	fmt.Fprintln(w, head)
	if t.Omitted {
		fmt.Fprintln(w, "   … earlier activity in this turn is not shown")
	}
	agent := o.Agent
	if t.Profile != "" {
		agent = t.Profile
		if t.Model != "" {
			agent += " / " + t.Model
		}
	}
	if agent == "" {
		agent = "agent"
	}
	prev := Kind(-1)
	for _, it := range t.Items {
		tight := (it.Kind == ToolUse || it.Kind == Thought) && (prev == ToolUse || prev == Thought)
		if !tight {
			fmt.Fprintln(w)
		}
		switch it.Kind {
		case Prompt:
			who := "you"
			if it.From != "" {
				who = it.From
			}
			fmt.Fprintf(w, "%s%s\n%s\n", who, clock(it.TS), indentText(it.Text))
		case Steering:
			who := "steering"
			if it.From != "" {
				who += " from " + it.From
			}
			mark := "◌"
			if it.Delivered {
				mark = "✓"
			}
			fmt.Fprintf(w, "↪ %s%s\n%s\n   %s %s\n", who, clock(it.TS), indentText(it.Text), mark, it.Status)
		case Reply:
			fmt.Fprintf(w, "◆ %s%s\n%s\n", agent, clock(it.TS), indentText(it.Text))
		case ToolUse:
			line := "   ▸ " + Headline(it.Tool)
			if it.Tool.Count <= 1 && it.Tool.Target != "" {
				line += " " + strings.Join(strings.Fields(it.Tool.Target), " ")
			}
			if aside := ToolAside(it.Tool); aside != "" {
				line += "  " + aside
			}
			fmt.Fprintln(w, line)
			if o.Expand {
				if d := strings.TrimSpace(it.Tool.Detail); d != "" && d != strings.TrimSpace(it.Tool.Target) {
					fmt.Fprintln(w, prefixLines(d, "     │ ", 12))
				}
				if out := strings.TrimSpace(it.Tool.Output); out != "" {
					fmt.Fprintln(w, prefixLines(out, "     │ ", 10))
				}
			}
		case Thought:
			if aside := ThoughtAside(it); aside != "" {
				fmt.Fprintf(w, "   ∴ %s  · %s\n", it.Text, aside)
			} else {
				fmt.Fprintf(w, "   ∴ %s\n", it.Text)
			}
		case Error:
			fmt.Fprintf(w, "   ✕ %s\n", it.Text)
		case Compaction:
			fmt.Fprintf(w, "   ── %s ──\n", CompactionLine(it.Compaction))
		case Note:
			fmt.Fprintf(w, "   %s\n", it.Text)
		}
		prev = it.Kind
	}
	if t.Hidden > 0 {
		fmt.Fprintf(w, "\n   · %d harness record(s) without readable content hidden (--raw shows the exact output)\n", t.Hidden)
	}
}

func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "  " + t.Local().Format("15:04:05")
}

func indentText(s string) string {
	return prefixLines(strings.TrimRight(s, "\n"), "   ", 0)
}

// prefixLines prefixes each line; maxLines > 0 keeps only the last lines.
func prefixLines(s, prefix string, maxLines int) string {
	lines := strings.Split(s, "\n")
	skipped := 0
	if maxLines > 0 && len(lines) > maxLines {
		skipped = len(lines) - maxLines
		lines = lines[skipped:]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(prefix+l, " ")
	}
	out := strings.Join(lines, "\n")
	if skipped > 0 {
		out = strings.TrimRight(prefix, " ") + fmt.Sprintf(" … %d earlier lines\n", skipped) + out
	}
	return out
}
