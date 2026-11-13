package contextview

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

func Section(key contract.ContextSectionKey, lines []string, maxChars int, full bool, rowLimit int) contract.ContextSection {
	if full {
		return contract.ContextSection{Key: key, Text: strings.Join(lines, "\n")}
	}
	var kept []string
	chars := 0
	for i, line := range lines {
		if rowLimit > 0 && i >= rowLimit {
			break
		}
		n := utf8.RuneCountInString(line)
		remaining := len(lines) - i - 1
		suffix := ""
		if remaining > 0 {
			suffix = fmt.Sprintf("\n%d more: context show", remaining)
		}
		sep := 0
		if len(kept) > 0 {
			sep = 1
		}
		if chars+sep+n+utf8.RuneCountInString(suffix) > maxChars {
			break
		}
		kept = append(kept, line)
		chars += sep + n
	}
	if more := len(lines) - len(kept); more > 0 {
		kept = append(kept, fmt.Sprintf("%d more: context show", more))
	}
	return contract.ContextSection{Key: key, Text: strings.Join(kept, "\n")}
}

// PRLine says a PR's state in a few words. It holds nothing from GitHub's free text, so the snapshot needs no wrapper.
func PRLine(p store.ContextPR) string {
	var parts []string
	if p.Draft {
		parts = append(parts, "draft")
	}
	switch p.CI {
	case contract.PrCISuccess:
		parts = append(parts, "CI passed")
	case contract.PrCICancelled:
		parts = append(parts, "CI cancelled")
	case contract.PrCIFailure:
		parts = append(parts, "CI failed")
	case contract.PrCIPending:
		parts = append(parts, "CI running")
	case contract.PrCINone:
		parts = append(parts, "no CI")
	default:
		parts = append(parts, "CI not read yet")
	}
	switch p.Review {
	case contract.PrApproved:
		parts = append(parts, "approved")
	case contract.PrChangesRequested:
		parts = append(parts, "changes requested")
	default:
		parts = append(parts, "not approved")
	}
	switch p.Merge {
	case contract.PrMergeClean:
		parts = append(parts, "mergeable")
	case contract.PrMergeConflict:
		parts = append(parts, "conflict")
	case contract.PrMergeBehind:
		parts = append(parts, "behind base")
	case contract.PrMergeBlocked:
		parts = append(parts, "merge blocked")
	}
	return strings.Join(parts, ", ")
}

// HoldsLine states what the session still holds. The turn being dispatched is not one of those: it is open because it is
// starting. A budget of 0 names everything.
func HoldsLine(held store.Held, budget int) string {
	var names []string
	used := 0
	for _, item := range held {
		if item.Kind == contract.ResponsibilityTurn {
			continue
		}
		name := room.ResponsibilityName(item)
		if used += len(name) + 2; budget > 0 && used > budget {
			break
		}
		names = append(names, name)
	}
	more := len(held) - held.Count(contract.ResponsibilityTurn) - len(names)
	if more > 0 {
		names = append(names, fmt.Sprintf("%d more: context show", more))
	}
	if len(names) == 0 {
		return ""
	}
	return "You still hold: " + strings.Join(names, ", ") + "."
}

// Excerpt fits text on one row of a section.
func Excerpt(text string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}
